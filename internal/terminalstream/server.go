package terminalstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/hookbridge"
	cloudruntime "fennec/internal/runtime"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type Starter func(context.Context, cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error)
type Attacher func(context.Context, cloudruntime.LocalTerminal) (cloudruntime.LocalTerminal, *exec.Cmd, error)
type Stopper func(context.Context, cloudruntime.LocalTerminal) (cloudruntime.LocalTerminal, error)
type Operator func(context.Context, cloudruntime.LocalOperation) (json.RawMessage, error)

type serverSession struct {
	ctx           context.Context
	cancel        context.CancelFunc
	window        *flowWindow
	input         chan []byte
	mu            sync.Mutex
	inputWriteMu  sync.Mutex
	file          *os.File
	closed        bool
	inputBudget   int
	rows, columns uint16
}

func Serve(ctx context.Context, input io.Reader, output io.Writer, start Starter, attachers ...Attacher) error {
	return serve(ctx, input, output, start, nil, nil, attachers...)
}

func ServeWithStop(ctx context.Context, input io.Reader, output io.Writer, start Starter, attach Attacher, stop Stopper) error {
	return serve(ctx, input, output, start, stop, nil, attach)
}

func ServeWithOperations(ctx context.Context, input io.Reader, output io.Writer, start Starter, attach Attacher, stop Stopper, operate Operator) error {
	return serve(ctx, input, output, start, stop, operate, attach)
}

func serve(ctx context.Context, input io.Reader, output io.Writer, start Starter, stopSession Stopper, operate Operator, attachers ...Attacher) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if closer, ok := input.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { closer.Close() })
		defer stop()
	}
	if closer, ok := output.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { closer.Close() })
		defer stop()
	}
	var writeMu sync.Mutex
	emitFrames := func(frames ...Frame) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		var batch bytes.Buffer
		encoder := json.NewEncoder(&batch)
		for _, frame := range frames {
			if err := encoder.Encode(frame); err != nil {
				return err
			}
		}
		_, err := output.Write(batch.Bytes())
		if err != nil {
			cancel()
		}
		return err
	}
	emit := func(frame Frame) error { return emitFrames(frame) }
	hooks := newHookRelay(ctx, emit)
	var sessionsMu sync.Mutex
	sessions := map[string]*serverSession{}
	var workers sync.WaitGroup
	defer func() {
		cancel()
		sessionsMu.Lock()
		for _, session := range sessions {
			session.cancel()
			session.window.close()
		}
		sessionsMu.Unlock()
		workers.Wait()
	}()
	capabilities := []string{"hook_bridge_v1"}
	if operate != nil {
		capabilities = append(capabilities, "reconcile_routes", "model_accounts", "native_accounts", "deferred_catalog", "model_catalog", "native_models", "native_model_set", "native_login", "native_account", "reconfigure_runtime", "configuration_gzip")
	}
	if err := emit(Frame{Type: "ready", Version: ProtocolVersion, Capabilities: capabilities}); err != nil {
		return err
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 262144)
	for scanner.Scan() {
		var frame Frame
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			return fmt.Errorf("invalid terminal frame")
		}
		if err := expandConfiguration(&frame); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if frame.Type == "ping" {
			if err := emit(Frame{Type: "pong"}); err != nil {
				return err
			}
			continue
		}
		if frame.Type == "launch" || frame.Type == "attach" || frame.Type == "stop" || frame.Type == "operation" {
			if frame.validateOpen() != nil || (frame.Type == "attach" && len(attachers) == 0) || (frame.Type == "stop" && stopSession == nil) || (frame.Type == "operation" && operate == nil) {
				return fmt.Errorf("invalid terminal launch")
			}
			sessionsMu.Lock()
			limit := 16
			if frame.Type == "stop" || frame.Type == "operation" {
				limit = 32
			}
			if sessions[frame.ID] != nil || len(sessions) >= limit {
				sessionsMu.Unlock()
				emit(Frame{Type: "error", ID: frame.ID, Message: "session already exists or capacity exhausted"})
				continue
			}
			sessionCtx, sessionCancel := context.WithCancel(ctx)
			session := &serverSession{ctx: sessionCtx, cancel: sessionCancel, window: newWindow(), input: make(chan []byte, 256), inputBudget: Window, rows: uint16(max(1, min(frame.Rows, 1000))), columns: uint16(max(1, min(frame.Columns, 1000)))}
			sessions[frame.ID] = session
			sessionsMu.Unlock()
			workers.Add(1)
			go func(frame Frame, session *serverSession) {
				opened := time.Now()
				defer workers.Done()
				defer func() {
					session.cancel()
					session.window.close()
					sessionsMu.Lock()
					delete(sessions, frame.ID)
					sessionsMu.Unlock()
				}()
				var terminal cloudruntime.LocalTerminal
				var command *exec.Cmd
				var err error
				if frame.HookBridge && (frame.Type == "launch" || frame.Type == "attach") {
					sessionID := frame.ID
					home, homeErr := os.UserHomeDir()
					if homeErr != nil {
						emit(Frame{Type: "error", ID: frame.ID, Message: "hook home unavailable"})
						return
					}
					if frame.Launch != nil {
						if err := hookbridge.SaveBindings(home, sessionID, frame.Launch.Hooks); err != nil {
							emit(Frame{Type: "error", ID: frame.ID, Message: "hook bindings unavailable"})
							return
						}
					}
					if frame.Terminal != nil {
						sessionID = frame.Terminal.ID
					}
					unbind, hookErr := hooks.subscribe(session.ctx, frame.ID, sessionID)
					if hookErr != nil {
						emit(Frame{Type: "error", ID: frame.ID, Message: "local hook relay unavailable"})
						return
					}
					defer unbind()
				}
				if frame.Type == "operation" {
					result, err := operate(session.ctx, *frame.Operation)
					if err != nil || len(result) > 4<<20 {
						emit(Frame{Type: "error", ID: frame.ID, Message: "operation not confirmed; inspect before retrying"})
					} else {
						emit(Frame{Type: "result", ID: frame.ID, Result: result, Timings: map[string]float64{"operation": float64(time.Since(opened).Microseconds()) / 1000}})
					}
					return
				}
				if frame.Type == "stop" {
					terminal, err = stopSession(session.ctx, *frame.Terminal)
					if err != nil {
						emit(Frame{Type: "error", ID: frame.ID, Message: "stop not confirmed; inspect session before retrying"})
					} else {
						emit(Frame{Type: "result", ID: frame.ID, Terminal: &terminal})
					}
					return
				}
				if frame.Type == "attach" {
					terminal, command, err = attachers[0](session.ctx, *frame.Terminal)
				} else {
					terminal, command, err = start(session.ctx, *frame.Launch)
				}
				if err != nil {
					emit(Frame{Type: "error", ID: frame.ID, Message: "remote launch failed; inspect session before retrying"})
					return
				}
				emit, finishReceipt := coalesceReceipt(emitFrames, Frame{Type: "receipt", ID: frame.ID, Terminal: &terminal})
				defer finishReceipt()
				session.mu.Lock()
				if command.Env == nil {
					command.Env = os.Environ()
				}
				terminalType := frame.Term
				if !regexp.MustCompile(`^[A-Za-z0-9+._-]{1,64}$`).MatchString(terminalType) {
					terminalType = "xterm-256color"
				}
				command.Env = append(command.Env, "TERM="+terminalType)
				file, err := pty.StartWithSize(command, &pty.Winsize{Rows: session.rows, Cols: session.columns})
				if err == nil {
					err = unix.SetNonblock(int(file.Fd()), true)
					session.file = file
				}
				session.mu.Unlock()
				if err != nil {
					if file != nil {
						file.Close()
						command.Process.Kill()
						command.Wait()
					}
					emit(Frame{Type: "error", ID: frame.ID, Message: "terminal attachment failed; reconnect to existing session"})
					return
				}
				stop := context.AfterFunc(session.ctx, func() { session.window.close(); syscall.Kill(-command.Process.Pid, syscall.SIGHUP) })
				defer stop()
				inputDone := make(chan struct{})
				go func() { defer close(inputDone); session.pumpInput(frame.ID, emit) }()
				probe := DisplayResponder{Facts: frame.Display}
				firstRead := float64(0)
				firstVisible := true
				emitData := func(data []byte) error {
					for len(data) > 0 {
						n := min(len(data), Chunk)
						var timings map[string]float64
						if firstVisible {
							firstVisible = false
							timings = map[string]float64{"first_pty_read": firstRead, "first_visible_output": float64(time.Since(opened).Microseconds()) / 1000}
						}
						if err := emit(Frame{Type: "data", ID: frame.ID, Data: data[:n], Timings: timings}); err != nil {
							return err
						}
						data = data[n:]
					}
					return nil
				}
				for {
					count := session.window.take(Chunk)
					if count == 0 {
						break
					}
					data := make([]byte, count)
					length, err := session.read(data)
					if length < count {
						session.window.add(count - length)
					}
					if length > 0 {
						if firstRead == 0 {
							firstRead = float64(time.Since(opened).Microseconds()) / 1000
						}
						shown, reply, refunded := probe.Filter(data[:length])
						if refunded > 0 {
							session.window.add(refunded)
						}
						if len(reply) > 0 {
							if err := session.write(reply); err != nil {
								break
							}
						}
						if err := emitData(shown); err != nil {
							break
						}
					}
					if err != nil {
						emitData(probe.Flush())
						break
					}
				}
				waitErr := command.Wait()
				code := 0
				if waitErr != nil {
					code = 1
					if status, ok := waitErr.(*exec.ExitError); ok {
						code = status.ExitCode()
						if code < 0 {
							code = 1
						}
					}
				}
				session.cancel()
				<-inputDone
				session.mu.Lock()
				session.closed = true
				file.Close()
				session.mu.Unlock()
				emit(Frame{Type: "exit", ID: frame.ID, Code: code})
			}(frame, session)
			continue
		}
		sessionsMu.Lock()
		session := sessions[frame.ID]
		sessionsMu.Unlock()
		if session == nil || session.ctx.Err() != nil {
			continue
		}
		if frame.Type == "focus" {
			frame.Type = "input"
			frame.Data = FocusReport(frame.Focused)
		}
		switch frame.Type {
		case "hook_reply":
			hooks.reply(frame)
		case "credit":
			if !session.window.add(frame.Bytes) {
				session.cancel()
				emit(Frame{Type: "error", ID: frame.ID, Message: "invalid output credit"})
			}
		case "input":
			session.mu.Lock()
			valid := len(frame.Data) > 0 && len(frame.Data) <= Chunk && len(frame.Data) <= session.inputBudget
			if valid {
				session.inputBudget -= len(frame.Data)
			}
			session.mu.Unlock()
			if valid {
				select {
				case session.input <- frame.Data:
				default:
					valid = false
				}
			}
			if !valid {
				session.cancel()
				emit(Frame{Type: "error", ID: frame.ID, Message: "input window exceeded"})
			}
		case "resize":
			session.mu.Lock()
			session.rows = uint16(max(1, min(frame.Rows, 1000)))
			session.columns = uint16(max(1, min(frame.Columns, 1000)))
			if session.file != nil && !session.closed {
				pty.Setsize(session.file, &pty.Winsize{Rows: session.rows, Cols: session.columns})
			}
			session.mu.Unlock()
		case "close":
			session.cancel()
		default:
			return fmt.Errorf("unsupported terminal frame")
		}
	}
	return scanner.Err()
}

func (session *serverSession) read(data []byte) (int, error) {
	for session.ctx.Err() == nil {
		poll := []unix.PollFd{{Fd: int32(session.file.Fd()), Events: unix.POLLIN}}
		if _, err := unix.Poll(poll, 100); err != nil && !errors.Is(err, syscall.EINTR) {
			return 0, err
		}
		if poll[0].Revents == 0 {
			continue
		}
		session.mu.Lock()
		length, err := unix.Read(int(session.file.Fd()), data)
		session.mu.Unlock()
		if length < 0 {
			length = 0
		}
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
			continue
		}
		if length == 0 && err == nil {
			err = io.EOF
		}
		return length, err
	}
	return 0, session.ctx.Err()
}

func (session *serverSession) pumpInput(id string, emit func(Frame) error) {
	for {
		select {
		case <-session.ctx.Done():
			return
		case data := <-session.input:
			length := len(data)
			if err := session.write(data); err != nil {
				return
			}
			data = nil
			if len(data) == 0 {
				session.mu.Lock()
				session.inputBudget += length
				session.mu.Unlock()
				if emit(Frame{Type: "input_credit", ID: id, Bytes: length}) != nil {
					return
				}
			}
		}
	}
}

func (session *serverSession) write(data []byte) error {
	session.inputWriteMu.Lock()
	defer session.inputWriteMu.Unlock()
	for len(data) > 0 && session.ctx.Err() == nil {
		poll := []unix.PollFd{{Fd: int32(session.file.Fd()), Events: unix.POLLOUT}}
		if _, err := unix.Poll(poll, 100); err != nil && !errors.Is(err, syscall.EINTR) {
			return err
		}
		if poll[0].Revents == 0 {
			continue
		}
		session.mu.Lock()
		written, err := unix.Write(int(session.file.Fd()), data)
		session.mu.Unlock()
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		data = data[written:]
	}
	if len(data) > 0 {
		return session.ctx.Err()
	}
	return nil
}
