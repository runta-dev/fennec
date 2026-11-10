package launcher

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/launchprofile"
	"fennec/internal/terminalinput"
	"fennec/internal/terminalstream"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type RemoteExitError struct{ Code int }

type DetachedError struct{}

func (DetachedError) Error() string { return "detached" }
func (DetachedError) ExitCode() int { return 0 }

func (err RemoteExitError) Error() string {
	return fmt.Sprintf("remote agent exited with status %d", err.Code)
}
func (err RemoteExitError) ExitCode() int { return err.Code }

type StreamLostError struct{ Receipt LaunchReceipt }

func (err StreamLostError) Error() string {
	return terminalDisconnectMessage()
}

func ExecuteStream(plan LaunchPlan, intent LaunchIntent, home string, ready func()) error {
	launchprofile.Record("stream.enter", time.Now(), 0)
	streamID := plan.StreamID
	if streamID == "" {
		streamID = intent.RequestID
	}
	if plan.StreamPath != filepath.Join(home, ".config/fennec/ipc", "terminal-"+streamID) || plan.SessionID != intent.RequestID || plan.Agent != intent.Agent {
		return fmt.Errorf("invalid terminal stream identity")
	}
	connection, err := net.DialTimeout("unix", plan.StreamPath, 5*time.Second)
	if err != nil {
		return err
	}
	defer connection.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	window := terminalstream.NewCreditWindow()
	var writeMu sync.Mutex
	encoder := json.NewEncoder(connection)
	send := func(frame terminalstream.Frame) error {
		frame.ID = streamID
		writeMu.Lock()
		defer writeMu.Unlock()
		return encoder.Encode(frame)
	}
	columns, rows, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		columns, rows = 120, 40
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	if interactive {
		state, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return err
		}
		defer term.Restore(int(os.Stdin.Fd()), state)
	}
	display, earlyInput, err := probeDisplay(os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := send(terminalstream.Frame{Type: "connect", Rows: rows, Columns: columns, Term: os.Getenv("TERM"), Display: display}); err != nil {
		return err
	}
	inputDone := make(chan struct{})
	var detached atomic.Bool
	stop := context.AfterFunc(ctx, func() { window.Close(); connection.Close() })
	defer stop()
	go func() {
		defer close(inputDone)
		buffer := make([]byte, terminalstream.Chunk)
		keys := terminalinput.Decoder{}
		for len(earlyInput) > 0 {
			count := window.Take(min(len(earlyInput), terminalstream.Chunk))
			if count == 0 {
				return
			}
			data, detach := keys.Feed(earlyInput[:count])
			earlyInput = earlyInput[count:]
			if len(data) > 0 {
				if err := send(terminalstream.Frame{Type: "input", Data: data}); err != nil {
					cancel()
					return
				}
			}
			if detach {
				detached.Store(true)
				return
			}
		}
		for ctx.Err() == nil {
			poll := []unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN}}
			if _, err := unix.Poll(poll, 100); err != nil && !errors.Is(err, syscall.EINTR) {
				cancel()
				return
			}
			if poll[0].Revents == 0 {
				if pending := keys.Flush(); len(pending) > 0 {
					if err := send(terminalstream.Frame{Type: "input", Data: pending}); err != nil {
						cancel()
						return
					}
				}
				continue
			}
			count := window.Take(len(buffer))
			if count == 0 {
				return
			}
			length, err := unix.Read(int(os.Stdin.Fd()), buffer[:count])
			if err != nil {
				window.Add(count)
				if errors.Is(err, syscall.EINTR) {
					continue
				}
				cancel()
				return
			}
			if length == 0 {
				if !interactive {
					window.Add(count - 1)
					send(terminalstream.Frame{Type: "input", Data: []byte{4}})
					return
				}
				detached.Store(true)
				cancel()
				return
			}
			if length < count {
				window.Add(count - length)
			}
			data, detach := keys.Feed(buffer[:length])
			if len(data) == 0 {
				continue
			}
			if detach {
				detached.Store(true)
				launchprofile.Record("stream.detach_input", time.Now(), 0)
			}
			if err := send(terminalstream.Frame{Type: "input", Data: data}); err != nil {
				cancel()
				return
			}
			if detach {
				launchprofile.Record("stream.detach_queued", time.Now(), 0)
				return
			}
		}
	}()
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-resize:
				columns, rows, err := term.GetSize(int(os.Stdin.Fd()))
				if err == nil {
					send(terminalstream.Frame{Type: "resize", Rows: rows, Columns: columns})
				}
			}
		}
	}()
	defer func() { cancel(); window.Close(); connection.Close(); <-inputDone }()
	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 4096), 262144)
	confirmed := false
	titles := &SessionTitleWriter{Writer: os.Stdout, SessionID: intent.RequestID}
	defer titles.Flush()
	var receipt LaunchReceipt
	connection.SetReadDeadline(time.Now().Add(90 * time.Second))
	for scanner.Scan() {
		var frame terminalstream.Frame
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.ID != streamID {
			return fmt.Errorf("invalid terminal frame")
		}
		switch frame.Type {
		case "receipt":
			launchprofile.Record("stream.receipt", time.Now(), 0)
			if confirmed || frame.Terminal == nil || frame.Terminal.ID != intent.RequestID || frame.Terminal.Agent != intent.Agent {
				return fmt.Errorf("terminal receipt identity mismatch")
			}
			receipt = LaunchReceipt{RuntimeID: plan.RuntimeID, SessionID: intent.RequestID, Agent: intent.Agent, Address: frame.Terminal.Address}
			var saveErr error
			if plan.Resume {
				saveErr = SaveReceipt(filepath.Join(home, ".config/fennec/launches"), receipt)
			} else {
				saveErr = savePlanReceipt(home, plan, intent, receipt)
			}
			if saveErr != nil {
				return saveErr
			}
			confirmed = true
			for _, stage := range []string{"journal_lock", "session_lookup", "workspace_prepare", "journal_sync", "herdr_spawn"} {
				if value, ok := frame.Terminal.Timings[stage]; ok {
					elapsed := time.Duration(value * float64(time.Millisecond))
					launchprofile.Record("guest."+stage, time.Now().Add(-elapsed), elapsed)
				}
			}
			connection.SetReadDeadline(time.Time{})
		case "data":
			if !confirmed || len(frame.Data) > terminalstream.Chunk {
				return fmt.Errorf("terminal output before receipt")
			}
			ready()
			if _, err := titles.Write(frame.Data); err != nil {
				return err
			}
			if len(frame.Data) > 0 {
				if err := send(terminalstream.Frame{Type: "credit", Bytes: len(frame.Data)}); err != nil {
					connection.SetReadDeadline(time.Now().Add(2 * time.Second))
				}
			}
		case "input_credit":
			if !window.Add(frame.Bytes) {
				return fmt.Errorf("invalid terminal input credit")
			}
		case "exit":
			if detached.Load() {
				return DetachedError{}
			}
			launchprofile.Record("stream.exit", time.Now(), 0)
			if !confirmed {
				return fmt.Errorf("terminal exited without receipt")
			}
			if frame.Code != 0 {
				return RemoteExitError{max(1, min(frame.Code, 255))}
			}
			return nil
		case "error":
			if confirmed {
				return StreamLostError{receipt}
			}
			return fmt.Errorf("%s; inspect request %s before retrying", frame.Message, intent.RequestID)
		default:
			return fmt.Errorf("unexpected terminal frame")
		}
	}
	if detached.Load() || (confirmed && ctx.Err() != nil) {
		return DetachedError{}
	}
	if confirmed && ctx.Err() == nil {
		return StreamLostError{receipt}
	}
	return fmt.Errorf("terminal launch outcome unknown; inspect request %s before retrying", intent.RequestID)
}
