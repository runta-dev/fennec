package terminalstream

import (
	"bufio"
	"context"
	"fennec/internal/launchprofile"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

type Transport struct {
	capabilities         map[string]bool
	initialized          atomic.Bool
	mu                   sync.Mutex
	writes               *writeScheduler
	stopWrites           context.CancelFunc
	input                io.WriteCloser
	stopProcess          func() error
	abortConnection      func() error
	probeSequence        uint64
	probePending         *healthProbe
	measuredPings        map[string]chan time.Time
	measuredPingWrites   int
	lastRead             atomic.Int64
	suspended            bool
	suspensionGeneration uint64
	deferredClose        map[string]bool
	sessions             map[string]*mailbox
	ready                chan struct{}
	done                 chan struct{}
	err                  error
}

type healthProbe struct {
	done chan struct{}
	ack  chan struct{}
	id   string
	err  error
	ctx  context.Context
}

type observedReader struct {
	io.Reader
	observed *atomic.Int64
}

func (reader observedReader) Read(buffer []byte) (int, error) {
	n, err := reader.Reader.Read(buffer)
	if n > 0 {
		reader.observed.Store(time.Now().UnixNano())
	}
	return n, err
}

type Session struct {
	transport *Transport
	id        string
	events    *mailbox
	once      sync.Once
	bulk      bool
}

func Start(command *exec.Cmd) (*Transport, error) {
	return start(command, nil)
}

func StartInitial(command *exec.Cmd, frame Frame) (*Transport, *Session, error) {
	if err := frame.validateOpen(); err != nil {
		return nil, nil, err
	}
	transport, err := start(command, &frame)
	if err != nil {
		return nil, nil, err
	}
	transport.mu.Lock()
	events := transport.sessions[frame.ID]
	transport.mu.Unlock()
	return transport, &Session{transport: transport, id: frame.ID, events: events}, nil
}

func start(command *exec.Cmd, initial *Frame) (*Transport, error) {
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		input.Close()
		output.Close()
		return nil, err
	}
	return startTransport(input, output, command.Wait, command.Process.Kill, initial), nil
}

// StartPipes runs the protocol over an in-process byte stream. Closing the
// transport closes both pipes.
func StartPipes(input io.WriteCloser, output io.ReadCloser) *Transport {
	return startTransport(input, output, func() error { return nil }, func() error { output.Close(); return input.Close() }, nil)
}

// StartSSH opens the resident protocol channel on an existing authenticated
// connection. Closing this transport closes only its channel, not the shared
// SSH client or any other sessions using it.
func StartSSH(client *ssh.Client, command string) (*Transport, error) {
	return StartSSHContext(context.Background(), client, command)
}

// A stalled channel-open/exec cannot outlive its connection owner's deadline.
// No remote agent has been launched by this protocol-service startup yet.
func StartSSHContext(ctx context.Context, client *ssh.Client, command string) (*Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	opened := launchprofile.Begin("terminal.channel_open")
	channel, requests, err := client.OpenChannel("session", nil)
	opened()
	if err != nil {
		return nil, err
	}
	// The versioned protocol's ready frame confirms execution. An independent
	// SSH exec acknowledgement adds a WAN round trip before the first request
	// without providing additional evidence. Queue exec before exposing stdin;
	// SSH preserves request/data ordering even when the server delays execution.
	if _, err := channel.SendRequest("exec", false, ssh.Marshal(struct{ Command string }{command})); err != nil {
		channel.Close()
		return nil, err
	}
	go ssh.DiscardRequests(requests)
	go io.Copy(io.Discard, channel.Stderr())
	return startTransport(channel, channel, func() error { return nil }, channel.Close, nil, client.Close), nil
}

func startTransport(input io.WriteCloser, output io.ReadCloser, wait func() error, stop func() error, initial *Frame, abort ...func() error) *Transport {
	transport := &Transport{input: input, stopProcess: stop, abortConnection: stop, sessions: map[string]*mailbox{}, ready: make(chan struct{}), done: make(chan struct{})}
	writeCtx, stopWrites := context.WithCancel(context.Background())
	transport.writes, transport.stopWrites = newWriteScheduler(writeCtx), stopWrites
	if len(abort) == 1 {
		transport.abortConnection = abort[0]
	}
	go transport.heartbeat()
	if initial != nil {
		transport.mu.Lock()
		transport.sessions[initial.ID] = newMailbox()
		transport.mu.Unlock()
	}
	go func() {
		defer stopWrites()
		defer close(transport.done)
		scanner := bufio.NewScanner(observedReader{output, &transport.lastRead})
		scanner.Buffer(make([]byte, 4096), (4<<20)+65536)
		packets := packetDecoder{}
		initialized := false
		for scanner.Scan() {
			frame, complete, err := packets.decode(scanner.Bytes())
			if err != nil {
				transport.fail(err)
				break
			}
			if !complete {
				continue
			}
			if err := expandTerminalData(&frame); err != nil {
				transport.fail(err)
				break
			}
			if !initialized {
				if frame.Type != "ready" || frame.Version != ProtocolVersion {
					transport.fail(fmt.Errorf("remote terminal protocol not ready"))
					break
				}
				initialized = true
				transport.mu.Lock()
				transport.capabilities = map[string]bool{}
				for _, capability := range frame.Capabilities {
					transport.capabilities[capability] = true
				}
				transport.mu.Unlock()
				if transport.capabilities[CapabilityTrafficPackets] {
					transport.writes.packets.enabled.Store(true)
					if err := transport.Send(Frame{Type: "traffic_config", Features: []string{CapabilityTrafficPackets}}); err != nil {
						transport.fail(err)
						break
					}
				}
				transport.initialized.Store(true)
				close(transport.ready)
				continue
			}
			if frame.Type == "pong" {
				received := time.Now()
				transport.mu.Lock()
				if ack := transport.measuredPings[frame.ID]; ack != nil {
					select {
					case ack <- received:
					default:
					}
				}
				if pending := transport.probePending; pending != nil && frame.ID == pending.id {
					select {
					case <-pending.ack:
					default:
						close(pending.ack)
					}
				}
				transport.mu.Unlock()
				continue
			}
			if (frame.Type == "data" && (len(frame.Data) == 0 || len(frame.Data) > Chunk)) || (frame.Type == "input_credit" && (frame.Bytes <= 0 || frame.Bytes > Window)) {
				transport.fail(fmt.Errorf("invalid remote terminal window"))
				break
			}
			transport.mu.Lock()
			box := transport.sessions[frame.ID]
			transport.mu.Unlock()
			if box == nil {
				continue
			}
			if err := box.push(frame); err != nil {
				box.close(err)
				transport.Send(Frame{Type: "close", ID: frame.ID})
			}
		}
		transport.fail(fmt.Errorf("remote terminal channel disconnected"))
		output.Close()
		wait()
	}()
	return transport
}

func (transport *Transport) IsReady() bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.initialized.Load() && transport.err == nil && !transport.suspended
}

// Suspend releases local consumers into their normal reconnect UI while
// retaining the authenticated SSH connection. Do not write close packets into
// an offline TCP socket: its retransmission backoff would delay restoration.
func (transport *Transport) Suspend() {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if !transport.suspended {
		transport.suspensionGeneration++
	}
	transport.suspended = true
	// A pre-outage heartbeat cannot establish post-outage liveness. Its late
	// reply must not satisfy the next recovery probe, even if still in flight.
	transport.probePending = nil
	for _, box := range transport.sessions {
		box.close(fmt.Errorf("terminal network suspended"))
	}
}

func (transport *Transport) Suspended() bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.suspended
}

// Resume verifies that the original connection survived a route interruption.
// A changed address or NAT mapping instead falls back to authenticated reconnect.
func (transport *Transport) Resume(ctx context.Context) error {
	if err := transport.Ready(ctx); err != nil {
		return err
	}
	if !transport.Supports("probe_id") {
		return fmt.Errorf("remote peer cannot confirm a fresh recovery probe")
	}
	if err := transport.probe(ctx, false); err != nil {
		return err
	}
	transport.mu.Lock()
	transport.suspended = false
	transport.mu.Unlock()
	return nil
}

func (transport *Transport) Supports(capability string) bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.capabilities[capability]
}

func (transport *Transport) heartbeat() {
	select {
	case <-transport.ready:
	case <-transport.done:
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-transport.done:
			return
		case <-ticker.C:
			transport.mu.Lock()
			generation, suspended := transport.suspensionGeneration, transport.suspended
			transport.mu.Unlock()
			if suspended {
				continue
			}
			probe, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := transport.Probe(probe)
			cancel()
			transport.mu.Lock()
			current := !transport.suspended && generation == transport.suspensionGeneration
			transport.mu.Unlock()
			if err != nil && current {
				launchprofile.Record("connection.retired_heartbeat", time.Now(), 0)
				transport.Abort()
				return
			}
		}
	}
}

// Done wakes the shared connection owner immediately after a protocol failure.
func (transport *Transport) Done() <-chan struct{} { return transport.done }

// Abort retires an unresponsive connection before sending channel EOF/close,
// which can themselves block behind a dead SSH socket. Normal Close still
// preserves peer channels on a healthy shared SSH connection.
func (transport *Transport) Abort() {
	transport.abortConnection()
	transport.Close()
}

// Probe confirms an actual round trip on the existing channel. Concurrent
// topology notifications and heartbeats share one pending probe.
func (transport *Transport) Probe(ctx context.Context) error {
	return transport.probe(ctx, true)
}

func (transport *Transport) probe(ctx context.Context, allowTraffic bool) error {
	if err := transport.Ready(ctx); err != nil {
		return err
	}
	started := time.Now().UnixNano()
	transport.mu.Lock()
	if transport.probePending == nil || transport.probePending.ctx.Err() != nil {
		pending := &healthProbe{done: make(chan struct{}), ack: make(chan struct{}), ctx: ctx}
		if transport.capabilities["probe_id"] {
			transport.probeSequence++
			pending.id = strconv.FormatUint(transport.probeSequence, 10)
		}
		transport.probePending = pending
		go func() {
			sent := make(chan error, 1)
			go func() { sent <- transport.Send(Frame{Type: "ping", ID: pending.id}) }()
			var err error
			select {
			case err = <-sent:
			case <-ctx.Done():
				err = ctx.Err()
			case <-transport.done:
				err = io.EOF
			}
			if err == nil {
				select {
				case <-pending.ack:
				case <-ctx.Done():
					err = ctx.Err()
				case <-transport.done:
					err = io.EOF
				}
			}
			transport.mu.Lock()
			pending.err = err
			if transport.probePending == pending {
				transport.probePending = nil
			}
			close(pending.done)
			transport.mu.Unlock()
		}()
	}
	pending := transport.probePending
	transport.mu.Unlock()
	select {
	case <-ctx.Done():
		// A large frame can delay its pong while bytes are still arriving.
		// Active transfer is evidence that the connection has not gone stale.
		if allowTraffic && transport.lastRead.Load() > started {
			return nil
		}
		return ctx.Err()
	case <-transport.done:
		return io.EOF
	case <-pending.done:
	}
	if allowTraffic && pending.err != nil && ctx.Err() != nil && transport.lastRead.Load() > started {
		return nil
	}
	return pending.err
}

func (transport *Transport) fail(err error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.err != nil {
		return
	}
	transport.err = err
	if transport.stopWrites != nil {
		transport.stopWrites()
	}
	transport.input.Close()
	for _, box := range transport.sessions {
		box.close(err)
	}
}

func (transport *Transport) Ready(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-transport.done:
		return fmt.Errorf("remote terminal channel failed")
	case <-transport.ready:
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.err
}

func (transport *Transport) Alive() bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.err == nil
}

func (transport *Transport) Send(frame Frame) error {
	if frame.Type == "launch" || frame.Type == "attach" || frame.Type == "socket" {
		frame.Features = append(append([]string(nil), frame.Features...), CapabilityWireWindow)
	}
	if frame.Type == "launch" || frame.Type == "attach" {
		// Old servers ignore the opt-in and send plain data. Only this daemon
		// decodes the compressed frames, so already running CLI shims also work.
		frame.OutputCompression = "gzip"
	}
	if frame.Type == "operation" && frame.Operation != nil && configurationCapability(frame.Operation.Kind) != "" && transport.Supports(configurationCapability(frame.Operation.Kind)) {
		var err error
		frame, err = compressConfiguration(frame)
		if err != nil {
			return err
		}
	}
	transport.mu.Lock()
	if transport.writes == nil {
		ctx, cancel := context.WithCancel(context.Background())
		transport.writes, transport.stopWrites = newWriteScheduler(ctx), cancel
	}
	transport.mu.Unlock()
	return transport.writeFrame(frame)
}

func (transport *Transport) writeFrame(frame Frame) error {
	transport.mu.Lock()
	if transport.suspended && frame.Type != "ping" {
		if frame.Type == "close" {
			if transport.deferredClose == nil {
				transport.deferredClose = make(map[string]bool)
			}
			transport.deferredClose[frame.ID] = true
			transport.mu.Unlock()
			return nil
		}
		transport.mu.Unlock()
		return fmt.Errorf("terminal network suspended")
	}
	deferred := transport.deferredClose
	transport.deferredClose = nil
	writes := transport.writes
	transport.mu.Unlock()
	for id := range deferred {
		if err := writes.sendFrames(transport.input, Frame{Type: "close", ID: id}); err != nil {
			return err
		}
	}
	return writes.sendFrames(transport.input, frame)
}

func (transport *Transport) Launch(frame Frame) (*Session, error) {
	if frame.validateOpen() != nil {
		return nil, fmt.Errorf("invalid terminal launch")
	}
	transport.mu.Lock()
	if transport.err != nil || transport.suspended {
		err := transport.err
		if err == nil {
			err = fmt.Errorf("terminal network suspended")
		}
		transport.mu.Unlock()
		return nil, err
	}
	if transport.sessions[frame.ID] != nil {
		transport.mu.Unlock()
		return nil, fmt.Errorf("duplicate terminal stream")
	}
	box := newMailbox()
	transport.sessions[frame.ID] = box
	transport.mu.Unlock()
	session := &Session{transport: transport, id: frame.ID, events: box, bulk: frame.Type == "socket"}
	if err := transport.Send(frame); err != nil {
		session.Close()
		return nil, fmt.Errorf("terminal launch outcome unknown: %w", err)
	}
	return session, nil
}

func (session *Session) Next(ctx context.Context) (Frame, error) {
	frame, err := session.events.next(ctx)
	if err == nil && (frame.Type == "exit" || frame.Type == "result" || frame.Type == "error") {
		session.complete()
	}
	return frame, err
}
func (session *Session) Send(frame Frame) error {
	switch frame.Type {
	case "image":
		if !session.transport.Supports(CapabilityImagePaste) {
			return fmt.Errorf("remote terminal does not support image paste")
		}
	case "input", "focus", "credit", "resize", "close", "hook_reply":
	default:
		return fmt.Errorf("invalid terminal control frame")
	}
	frame.ID = session.id
	if frame.Type == "input" && session.bulk {
		frame.bulk = true
	}
	return session.transport.Send(frame)
}
func (session *Session) Close() {
	session.release(true)
}

// Completed operations have already released their remote worker. Sending a
// close after the final result creates an unnecessary small SSH packet and can
// delay the next request. In-flight cancellation still sends close.
func (session *Session) complete() {
	session.release(false)
}

func (session *Session) release(cancelRemote bool) {
	session.once.Do(func() {
		if cancelRemote {
			session.Send(Frame{Type: "close"})
		}
		session.transport.mu.Lock()
		delete(session.transport.sessions, session.id)
		session.transport.mu.Unlock()
		session.events.close(io.EOF)
	})
}
func (transport *Transport) Close() {
	transport.fail(io.EOF)
	transport.stopProcess()
	<-transport.done
}
