package terminalstream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

type Transport struct {
	lastPong atomic.Int64
	mu       sync.Mutex
	writeMu  sync.Mutex
	input    io.WriteCloser
	command  *exec.Cmd
	sessions map[string]*mailbox
	ready    chan struct{}
	done     chan struct{}
	err      error
}

type Session struct {
	transport *Transport
	id        string
	events    *mailbox
	once      sync.Once
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
	return transport, &Session{transport: transport, id: frame.ID, events: transport.sessions[frame.ID]}, nil
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
	transport := &Transport{input: input, command: command, sessions: map[string]*mailbox{}, ready: make(chan struct{}), done: make(chan struct{})}
	go transport.heartbeat()
	if initial != nil {
		transport.sessions[initial.ID] = newMailbox()
	}
	go func() {
		defer close(transport.done)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), (4<<20)+65536)
		initialized := false
		for scanner.Scan() {
			var frame Frame
			if json.Unmarshal(scanner.Bytes(), &frame) != nil {
				transport.fail(fmt.Errorf("invalid remote terminal frame"))
				break
			}
			if !initialized {
				if frame.Type != "ready" || frame.Version != ProtocolVersion {
					transport.fail(fmt.Errorf("remote terminal protocol not ready"))
					break
				}
				initialized = true
				transport.lastPong.Store(time.Now().UnixNano())
				close(transport.ready)
				continue
			}
			if frame.Type == "pong" {
				transport.lastPong.Store(time.Now().UnixNano())
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
		command.Wait()
	}()
	return transport, nil
}

func (transport *Transport) heartbeat() {
	select {
	case <-transport.ready:
	case <-transport.done:
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var sent chan error
	for {
		select {
		case <-transport.done:
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, transport.lastPong.Load())) >= 120*time.Second {
				transport.Close()
				return
			}
			if sent == nil {
				sent = make(chan error, 1)
				go func(result chan error) { result <- transport.Send(Frame{Type: "ping"}) }(sent)
			}
		case err := <-sent:
			sent = nil
			if err != nil {
				transport.Close()
				return
			}
		}
	}
}

func (transport *Transport) fail(err error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.err != nil {
		return
	}
	transport.err = err
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
	transport.writeMu.Lock()
	defer transport.writeMu.Unlock()
	return json.NewEncoder(transport.input).Encode(frame)
}

func (transport *Transport) Launch(frame Frame) (*Session, error) {
	if frame.validateOpen() != nil {
		return nil, fmt.Errorf("invalid terminal launch")
	}
	transport.mu.Lock()
	if transport.err != nil {
		err := transport.err
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
	session := &Session{transport: transport, id: frame.ID, events: box}
	if err := transport.Send(frame); err != nil {
		session.Close()
		return nil, fmt.Errorf("terminal launch outcome unknown: %w", err)
	}
	return session, nil
}

func (session *Session) Next(ctx context.Context) (Frame, error) { return session.events.next(ctx) }
func (session *Session) Send(frame Frame) error {
	switch frame.Type {
	case "input", "credit", "resize", "close":
	default:
		return fmt.Errorf("invalid terminal control frame")
	}
	frame.ID = session.id
	return session.transport.Send(frame)
}
func (session *Session) Close() {
	session.once.Do(func() {
		session.Send(Frame{Type: "close"})
		session.transport.mu.Lock()
		delete(session.transport.sessions, session.id)
		session.transport.mu.Unlock()
		session.events.close(io.EOF)
	})
}
func (transport *Transport) Close() {
	transport.fail(io.EOF)
	transport.command.Process.Kill()
	<-transport.done
}
