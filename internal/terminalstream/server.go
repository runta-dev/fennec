package terminalstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/audit"
	"fennec/internal/clipboardbridge"
	"fennec/internal/cloudops"
	"fennec/internal/hookbridge"
	cloudruntime "fennec/internal/runtime"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/sys/unix"
)

type Starter func(context.Context, cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error)
type Attacher func(context.Context, cloudruntime.LocalTerminal) (cloudruntime.LocalTerminal, *exec.Cmd, error)
type Stopper func(context.Context, cloudruntime.LocalTerminal) (cloudruntime.LocalTerminal, error)
type Operator func(context.Context, cloudops.Operation) (json.RawMessage, error)

// SocketDialer opens the named remote socket for a "socket" stream. Only
// targets accepted by Frame.validateOpen reach it.
type SocketDialer func(context.Context, string) (net.Conn, error)

type socketDeviceKey struct{}

// SocketDevice is the Mac device ID the client sent when opening a socket
// stream, or empty for clients that did not send one.
func SocketDevice(ctx context.Context) string {
	device, _ := ctx.Value(socketDeviceKey{}).(string)
	return device
}

type serverSession struct {
	// conf merges output that waits for credit; nil when not attached.
	conf                     *conflator
	deviceRelay              bool
	commandStream            bool
	ctx                      context.Context
	cancel                   context.CancelFunc
	window                   *flowWindow
	wire                     *wireWindow
	input                    chan Frame
	images                   bool
	imageAgent, imageSession string
	imageClipboard           bool
	mu                       sync.Mutex
	inputWriteMu             sync.Mutex
	file                     *os.File
	fd                       int
	conn                     net.Conn
	closed                   bool
	inputBudget              int
	rows, columns            uint16
	echo                     *echoAcker
	cadence                  *cadence
}

func Serve(ctx context.Context, input io.Reader, output io.Writer, start Starter, attachers ...Attacher) error {
	return serve(ctx, input, output, start, nil, nil, nil, attachers...)
}

func ServeWithStop(ctx context.Context, input io.Reader, output io.Writer, start Starter, attach Attacher, stop Stopper) error {
	return serve(ctx, input, output, start, stop, nil, nil, attach)
}

func ServeWithOperations(ctx context.Context, input io.Reader, output io.Writer, start Starter, attach Attacher, stop Stopper, operate Operator) error {
	return serve(ctx, input, output, start, stop, operate, nil, attach)
}

// ServeWithSockets additionally relays byte streams to whitelisted remote sockets.
func ServeWithSockets(ctx context.Context, input io.Reader, output io.Writer, start Starter, attach Attacher, stop Stopper, operate Operator, dial SocketDialer) error {
	return serve(ctx, input, output, start, stop, operate, dial, attach)
}

func serve(ctx context.Context, input io.Reader, output io.Writer, start Starter, stopSession Stopper, operate Operator, dial SocketDialer, attachers ...Attacher) error {
	return serveWithIdleTimeout(ctx, input, output, start, stopSession, operate, dial, time.Minute, attachers...)
}

func serveWithIdleTimeout(ctx context.Context, input io.Reader, output io.Writer, start Starter, stopSession Stopper, operate Operator, dial SocketDialer, idleTimeout time.Duration, attachers ...Attacher) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	touch := serverLease(ctx, cancel, idleTimeout)
	if closer, ok := input.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { closer.Close() })
		defer stop()
	}
	if closer, ok := output.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { closer.Close() })
		defer stop()
	}
	writes := newWriteScheduler(ctx)
	emitFrames := func(frames ...Frame) error {
		err := writes.sendFrames(output, frames...)
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
	capabilities := []string{"claude_settings_v1", "hook_bridge_v1", CapabilityImagePaste, CapabilityImageWebP, "probe_id", CapabilityTrafficPackets}
	if guard, ok := output.(interface{ TCPQueueGuarded() bool }); ok && guard.TCPQueueGuarded() {
		capabilities = append(capabilities, CapabilityTCPOutputGuard)
	}
	if operate != nil {
		capabilities = append(capabilities, "reconcile_routes", "model_accounts", "native_accounts", "deferred_catalog", "model_catalog", "native_models", "native_model_set", "native_login", "native_account", "reconfigure_runtime", "configuration_gzip", "model_accounts_gzip", "system_metrics", "native_credential_set", "native_credential_watch", "connector_prepare", "connector_health", "mac_online", "magpie_credentials_sync", "preparation_health", "tailnet")
	}
	if dial != nil {
		capabilities = append(capabilities, CapabilityCodexAppServer, CapabilityCodexAppServerEarlyInput, CapabilitySessionImport, CapabilityDeviceRelay, CapabilityCommand, CapabilityMagpieWeb, CapabilityPreview, CapabilityBoard, CapabilityBoardImages, CapabilityBoardWebP, CapabilityThreadSession, CapabilityCloudArchive)
	}
	if err := emit(Frame{Type: "ready", Version: ProtocolVersion, Capabilities: capabilities}); err != nil {
		return err
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 262144)
	packets := packetDecoder{maximum: 262143}
	for scanner.Scan() {
		frame, complete, err := packets.decode(scanner.Bytes())
		if err != nil {
			return err
		}
		if !complete {
			touch()
			continue
		}
		if err := expandConfiguration(&frame); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		touch()
		if frame.Type == "traffic_config" {
			for _, feature := range frame.Features {
				if feature == CapabilityTrafficPackets {
					writes.packets.enabled.Store(true)
				}
			}
			continue
		}
		if frame.Type == "ping" {
			if err := emit(Frame{Type: "pong", ID: frame.ID}); err != nil {
				return err
			}
			continue
		}
		if (frame.Type == "launch" || frame.Type == "attach") && frame.Display != nil {
			if home, err := os.UserHomeDir(); err == nil {
				RememberDisplay(home, frame.Display)
			}
		}
		if frame.Type == "launch" || frame.Type == "attach" || frame.Type == "stop" || frame.Type == "operation" || frame.Type == "socket" {
			if frame.validateOpen() != nil || (frame.Type == "socket" && dial == nil) || (frame.Type == "attach" && len(attachers) == 0) || (frame.Type == "stop" && stopSession == nil) || (frame.Type == "operation" && operate == nil) {
				return fmt.Errorf("invalid terminal launch")
			}
			sessionsMu.Lock()
			deviceRelay := frame.Type == "socket" && frame.Target == SocketDeviceRelay
			commandStream := frame.Type == "socket" && frame.Target == SocketCommand
			used := 0
			for _, active := range sessions {
				if active.deviceRelay == deviceRelay && active.commandStream == commandStream {
					used++
				}
			}
			// Fennec Desktop keeps one attach stream open per cloud tab, so a
			// person with many sessions open needs well over 16. Stops and
			// operations share this count and get headroom above it, so open
			// tabs can never starve the control plane.
			limit := 64
			if frame.Type == "stop" || frame.Type == "operation" {
				limit = 96
			}
			if commandStream {
				// Remote commands keep their own, smaller allowance.
				limit = 16
			}
			if deviceRelay {
				// Eight parked bridges, 64 active connections and eight retiring
				// streams whose close frames can race replacement opens.
				limit = 80
			}
			if sessions[frame.ID] != nil || used >= limit {
				sessionsMu.Unlock()
				emit(Frame{Type: "error", ID: frame.ID, Message: "session already exists or capacity exhausted"})
				continue
			}
			sessionCtx, sessionCancel := context.WithCancel(ctx)
			session := &serverSession{deviceRelay: deviceRelay, commandStream: commandStream, ctx: sessionCtx, cancel: sessionCancel, window: newWindow(), input: make(chan Frame, 256), images: frame.Type == "launch" || frame.Type == "attach", inputBudget: Window, rows: 40, columns: 120}
			if rows, columns, ok := viewerSize(frame.Rows, frame.Columns); ok {
				session.rows, session.columns = rows, columns
			}
			if hasFeature(frame.Features, CapabilityWireWindow) {
				session.wire = newWireWindow()
				if frame.Type == "socket" && writes.packets.enabled.Load() {
					// Paced bulk streams need enough outstanding bytes to use a
					// high-RTT link: their window follows the round trip sized
					// for the egress ceiling (socketWindow). The shared
					// scheduler still paces every bulk packet.
					session.wire = newSocketWireWindow()
				}
				context.AfterFunc(sessionCtx, session.wire.close)
			}
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
				if frame.Type == "socket" {
					session.relaySocket(frame, dial, emit)
					return
				}
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
						emit(Frame{Type: "error", ID: frame.ID, Message: operationFailure(err)})
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
					var reference cloudruntime.SessionReferenceError
					if errors.As(err, &reference) {
						emit(Frame{Type: "error", ID: frame.ID, ErrorCode: reference.Code, Message: reference.Message})
						return
					}
					emit(Frame{Type: "error", ID: frame.ID, Message: "remote launch failed; inspect session before retrying"})
					return
				}
				receiptCapabilities := []string{CapabilityImagePaste, CapabilityImageWebP}
				if session.wire != nil {
					receiptCapabilities = append(receiptCapabilities, CapabilityWireWindow)
				}
				if hasFeature(frame.Features, CapabilityEchoAck) {
					session.echo = newEchoAcker()
					receiptCapabilities = append(receiptCapabilities, CapabilityEchoAck)
				}
				if hasFeature(frame.Features, CapabilityInputCadence) {
					session.cadence = &cadence{}
					receiptCapabilities = append(receiptCapabilities, CapabilityInputCadence)
				}
				emit, finishReceipt := coalesceReceipt(emitFrames, Frame{Type: "receipt", ID: frame.ID, Terminal: &terminal, Capabilities: receiptCapabilities})
				defer finishReceipt()
				session.mu.Lock()
				session.imageAgent, session.imageSession = terminal.Agent, terminal.ID
				session.imageClipboard = terminal.Labels[clipboardbridge.Label] == clipboardbridge.Version
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
					session.file, session.fd, err = pollablePTY(file)
					if err == nil {
						file.Close()
					}
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
				finishAttachment := session.watchAttachment(command)
				inputDone := make(chan struct{})
				go func() { defer close(inputDone); session.pumpInput(frame.ID, emit) }()
				if session.echo != nil {
					go session.echo.run(session.ctx, frame.ID, emit)
				}
				probe := DisplayResponder{Facts: frame.Display}
				var noop NoopFrameFilter
				// Reading and sending are separate: the reader keeps reading the
				// attachment while the sender waits for credit or for the merge
				// interval, and output that piles up meanwhile is merged (see
				// conflator).
				conf := newConflator(int(session.rows), int(session.columns))
				session.mu.Lock()
				session.conf = conf
				session.mu.Unlock()
				var firstReadUS atomic.Int64
				firstVisible := true
				emitData := func(data []byte) error {
					for len(data) > 0 {
						n := session.window.take(min(len(data), OutputChunk))
						if n == 0 {
							return session.ctx.Err()
						}
						var timings map[string]float64
						if firstVisible {
							firstVisible = false
							timings = map[string]float64{"first_pty_read": float64(firstReadUS.Load()) / 1000, "first_visible_output": float64(time.Since(opened).Microseconds()) / 1000}
						}
						out := Frame{Type: "data", ID: frame.ID, Data: data[:n], Timings: timings}
						if frame.OutputCompression == "gzip" {
							out = compressTerminalData(out)
						}
						finishOutput := session.echo.beginOutput()
						err := session.emitOutput(out, n, emit)
						finishOutput()
						if err != nil {
							return err
						}
						data = data[n:]
					}
					return nil
				}
				var sendFailed atomic.Bool
				sent := make(chan struct{})
				go func() {
					defer close(sent)
					var lastTake time.Time
					for conf.Wait() {
						// At most one take per outputMergeInterval: output
						// arriving within it merges into one frame (see
						// conflator). Meanwhile it counts as queued, so no echo
						// acknowledgement overtakes it.
						done := func() {}
						if wait := time.Until(lastTake.Add(outputMergeInterval)); wait > 0 {
							done = session.echo.beginOutput()
							time.Sleep(wait)
						}
						data := conf.Take()
						lastTake = time.Now()
						err := emitData(data)
						done()
						if err != nil {
							sendFailed.Store(true)
							conf.Close() // unblock the reader
							return
						}
					}
				}()
				buffer := make([]byte, Chunk)
				for !sendFailed.Load() {
					length, err := session.read(buffer)
					if length > 0 {
						firstReadUS.CompareAndSwap(0, max(1, time.Since(opened).Microseconds()))
						// Credit is taken when sending; what the filters drop was
						// never counted.
						shown, reply, _ := probe.Filter(buffer[:length])
						if len(reply) > 0 {
							if err := session.write(reply); err != nil {
								break
							}
						}
						shown, _ = noop.Filter(shown)
						if len(shown) > 0 {
							conf.Write(shown)
						}
					}
					if err != nil {
						conf.Write(noop.Flush())
						conf.Write(probe.Flush())
						break
					}
				}
				conf.Close()
				<-sent
				session.mu.Lock()
				session.conf = nil
				session.mu.Unlock()
				waitErr := command.Wait()
				finishAttachment()
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
				session.closePTY()
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
			if (session.wire != nil && !session.wire.ack(frame.Bytes)) || !session.window.add(frame.Bytes) {
				session.cancel()
				emit(Frame{Type: "error", ID: frame.ID, Message: "invalid output credit"})
			}
		case "input", "image":
			session.mu.Lock()
			valid := (frame.Type != "image" || session.images) && len(frame.Data) > 0 && len(frame.Data) <= Chunk && len(frame.Data) <= session.inputBudget
			if valid {
				session.inputBudget -= len(frame.Data)
			}
			session.mu.Unlock()
			if valid {
				select {
				case session.input <- frame:
				default:
					valid = false
				}
			}
			if !valid {
				session.cancel()
				emit(Frame{Type: "error", ID: frame.ID, Message: "input window exceeded"})
			}
		case "resize":
			rows, columns, ok := viewerSize(frame.Rows, frame.Columns)
			if !ok {
				// A viewer that is hidden or not laid out yet reports no real size.
				break
			}
			session.mu.Lock()
			session.rows, session.columns = rows, columns
			if session.conf != nil {
				session.conf.Resize(int(session.rows), int(session.columns))
			}
			if session.file != nil && !session.closed {
				session.resizePTY()
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
	if err := session.ctx.Err(); err != nil {
		return 0, err
	}
	// File.Read parks the goroutine in the runtime poller. Do not hold mu
	// while waiting: closePTY must be able to interrupt reads on cancellation.
	return session.file.Read(data)
}

// The smallest viewer size a remote terminal takes. Herdr gives a terminal
// its newest viewer's size, so a viewer reporting 0x0 or 1x1 (a view that is
// hidden or not laid out yet) shrank the agent to one cell: Claude Code
// redrew its whole screen as "─", and the thread's peek of it was empty
// (FEN-37). The phone applies the same floor (mobile Attach/Resize).
const (
	minViewerColumns = 20
	minViewerRows    = 5
)

// viewerSize is a viewer's reported size, or false when it is too small to be real.
func viewerSize(rows, columns int) (uint16, uint16, bool) {
	if rows < minViewerRows || columns < minViewerColumns {
		return 0, 0, false
	}
	return uint16(min(rows, 1000)), uint16(min(columns, 1000)), true
}

// Caller holds session.mu; resize must preserve O_NONBLOCK just like IO.
func (session *serverSession) resizePTY() error {
	return unix.IoctlSetWinsize(session.fd, unix.TIOCSWINSZ, &unix.Winsize{Row: session.rows, Col: session.columns})
}

func (session *serverSession) pumpInput(id string, emit func(Frame) error) {
	var image imageUpload
	defer image.reset()
	for {
		select {
		case <-session.ctx.Done():
			return
		case frame := <-session.input:
			session.mu.Lock()
			focused := session.imageSession
			session.mu.Unlock()
			if focused != "" {
				FocusSession(focused)
			}
			length := len(frame.Data)
			if frame.Type == "image" {
				path, err := image.accept(frame)
				if err != nil {
					if emit(Frame{Type: "paste_error", ID: id, Message: "Could not paste image, try copying it again"}) != nil {
						return
					}
				} else if path != "" {
					if session.imageAgent == "kimi" {
						message := "Could not paste image, focus the message composer and try again"
						var pasteErr error
						if !session.imageClipboard {
							pasteErr = fmt.Errorf("legacy Kimi session")
							message = "Start a new Kimi session to enable image paste"
						} else {
							home, homeErr := os.UserHomeDir()
							pasteErr = homeErr
							if pasteErr == nil {
								pasteErr = clipboardbridge.Deliver(session.ctx, home, session.imageSession, path, func() error { return session.write([]byte{22}) })
							}
						}
						os.Remove(path)
						if pasteErr != nil {
							if emit(Frame{Type: "paste_error", ID: id, Message: message}) != nil {
								return
							}
						}
					} else {
						if err := session.write(imagePaste(path)); err != nil {
							os.Remove(path)
							return
						}
					}
				}
				if err != nil || path != "" {
					// The upload is complete or rejected and its paste written.
					session.echo.input(frame.Seq)
				}
			} else {
				image.reset()
				if session.cadence != nil && frame.DelayMS > 0 {
					if pause := session.cadence.wait(time.Now(), time.Duration(frame.DelayMS)*time.Millisecond); pause > 0 {
						timer := time.NewTimer(pause)
						select {
						case <-session.ctx.Done():
							timer.Stop()
							return
						case <-timer.C:
						}
					}
				}
				// Record the input before writing it: a fast agent can echo
				// before write returns, and that output must count.
				session.echo.input(frame.Seq)
				if err := session.write(frame.Data); err != nil {
					return
				}
				if session.cadence != nil {
					session.cadence.wrote(time.Now())
				}
			}
			session.mu.Lock()
			session.inputBudget += length
			session.mu.Unlock()
			if emit(Frame{Type: "input_credit", ID: id, Bytes: length}) != nil {
				return
			}
		}
	}
}

func (session *serverSession) write(data []byte) error {
	session.inputWriteMu.Lock()
	defer session.inputWriteMu.Unlock()
	session.mu.Lock()
	conn := session.conn
	session.mu.Unlock()
	if conn != nil {
		_, err := conn.Write(data)
		return err
	}
	if err := session.ctx.Err(); err != nil {
		return err
	}
	_, err := session.file.Write(data)
	return err
}

// relaySocket carries raw bytes between a whitelisted remote socket and the
// stream. Output obeys the same credit window as a PTY; input reuses the
// session input budget, so one busy socket cannot starve other streams.
func (session *serverSession) relaySocket(frame Frame, dial SocketDialer, emit func(Frame) error) {
	conn, err := dial(context.WithValue(session.ctx, socketDeviceKey{}, frame.Message), frame.Target)
	if err != nil {
		emit(Frame{Type: "error", ID: frame.ID, Message: "remote socket unavailable"})
		return
	}
	session.mu.Lock()
	session.conn = conn
	session.mu.Unlock()
	stop := context.AfterFunc(session.ctx, func() { session.window.close(); conn.Close() })
	defer stop()
	receipt := Frame{Type: "receipt", ID: frame.ID}
	if labeled, ok := conn.(interface{ StreamLabel() string }); ok {
		receipt.Message = labeled.StreamLabel()
	}
	var encoder *zstd.Encoder
	var compressed bytes.Buffer
	if frame.Term == SocketCompressionZstd {
		encoder, err = zstd.NewWriter(&compressed, zstd.WithWindowSize(SocketCompressionWindow), zstd.WithEncoderConcurrency(1))
		if err == nil {
			receipt.Term = SocketCompressionZstd
			defer encoder.Close()
		}
	}
	if emit(receipt) != nil {
		session.cancel()
		return
	}
	inputDone := make(chan struct{})
	go func() { defer close(inputDone); session.pumpInput(frame.ID, emit) }()
	if encoder != nil {
		session.relayCompressed(frame.ID, conn, encoder, &compressed, emit)
		session.cancel()
		conn.Close()
		<-inputDone
		emit(Frame{Type: "exit", ID: frame.ID})
		return
	}
	buffer := make([]byte, OutputChunk)
	for {
		count := session.window.take(OutputChunk)
		if count == 0 {
			break
		}
		length, err := conn.Read(buffer[:count])
		if length < count {
			session.window.add(count - length)
		}
		if length > 0 {
			if session.emitOutput(Frame{Type: "data", ID: frame.ID, Data: append([]byte(nil), buffer[:length]...)}, length, emit) != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	session.cancel()
	conn.Close()
	<-inputDone
	emit(Frame{Type: "exit", ID: frame.ID})
}

// operationFailure keeps the cause of a failed remote operation for private
// diagnostics: one line, credentials redacted, bounded. Never a raw dump.
func operationFailure(err error) string {
	const base = "operation not confirmed; inspect before retrying"
	if err == nil {
		return base + ": result too large"
	}
	cause := audit.Redact(strings.Join(strings.Fields(err.Error()), " "), 240)
	if cause == "" {
		return base
	}
	return base + ": " + cause
}

// relayCompressed sends the socket's output as one zstd stream, flushed after
// every read so each message is decodable on arrival. The large window lets
// repeated replies (ChatGPT re-reads its apps tool catalog) compress to almost
// nothing. Credits count compressed bytes, so the window stays 64 KiB and the
// stream cannot crowd out terminals sharing the channel.
func (session *serverSession) relayCompressed(id string, conn net.Conn, encoder *zstd.Encoder, compressed *bytes.Buffer, emit func(Frame) error) {
	send := func() bool {
		data := compressed.Bytes()
		for len(data) > 0 {
			count := session.window.take(min(len(data), OutputChunk))
			if count == 0 {
				return false
			}
			if session.emitOutput(Frame{Type: "data", ID: id, Data: append([]byte(nil), data[:count]...)}, count, emit) != nil {
				return false
			}
			data = data[count:]
		}
		compressed.Reset()
		return true
	}
	buffer := make([]byte, 64<<10)
	for {
		length, err := conn.Read(buffer)
		if length > 0 {
			if _, writeErr := encoder.Write(buffer[:length]); writeErr != nil {
				return
			}
			if encoder.Flush() != nil || !send() {
				return
			}
		}
		if err != nil {
			if encoder.Close() == nil {
				send()
			}
			return
		}
	}
}

// FocusSession is called with a session's ID for every input the user sends
// it (keys, mouse, pastes), so the resident can weight that session's agent
// above the others while the user works in it (cpusched.Focus).
var FocusSession = func(id string) {}
