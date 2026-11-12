package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"
)

// Bridge timing.
const (
	// StartWait bounds the wait for a daemon the bridge started.
	StartWait = 10 * time.Second
	// StartPoll is how often the bridge tries the socket while it waits.
	StartPoll = 10 * time.Millisecond
	// EmbeddedEnv marks a host app (Fennec.app) that starts the daemon
	// itself; the bridge then only connects.
	EmbeddedEnv = "FENNEC_CUA_EMBEDDED"
)

// BridgeConfig configures the stdio bridge.
type BridgeConfig struct {
	// Socket is the daemon's MCP socket.
	Socket string
	// Embedded forbids starting the daemon: with none answering, the
	// bridge fails at once.
	Embedded bool
	// Start starts the daemon. exited, when not nil, delivers the daemon's
	// exit should it end before it answers.
	Start func() (exited <-chan error, err error)
	// StartWait and Poll default to StartWait and StartPoll.
	StartWait time.Duration
	Poll      time.Duration
	// Local answers initialize, ping, tools/list and the LocalTools calls
	// while no daemon is connected, so they never start one. Nil starts the
	// daemon for every request.
	Local Handler
	// LocalTools are the tools Local answers; empty means status.
	LocalTools []string
	// Info describes the server for a locally answered initialize.
	Info Info
	// Dial overrides how the socket is reached (tests).
	Dial func(socket string) (net.Conn, error)
}

// ErrEmbeddedNoDaemon is the embedded-mode failure when no daemon answers.
var ErrEmbeddedNoDaemon = errors.New("no daemon answers and " + EmbeddedEnv + "=1 (embedded mode) leaves starting it to the host app; nothing was started")

// NoDaemon reports whether a dial error means nothing listens on the socket
// (no file, or a file nobody accepts on): the only case in which the bridge
// may start a daemon. Any other error (permission denied, a socket of
// another user) must be reported instead.
func NoDaemon(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

const bridgeInitID = `"fennec-cua-bridge-init"`

// Bridge relays MCP between a stdio client (in, out) and the daemon's
// socket. It answers initialize, tools/list and status itself while no
// daemon runs and starts one only for the first other request. Requests
// sent to a daemon whose connection then drops are answered with an error
// and never sent again, since they may have landed. Bridge returns when in
// ends.
func Bridge(ctx context.Context, in io.Reader, out io.Writer, cfg BridgeConfig) error {
	if cfg.StartWait <= 0 {
		cfg.StartWait = StartWait
	}
	if cfg.Poll <= 0 {
		cfg.Poll = StartPoll
	}
	if cfg.Dial == nil {
		cfg.Dial = func(socket string) (net.Conn, error) { return net.Dial("unix", socket) }
	}
	if len(cfg.LocalTools) == 0 {
		cfg.LocalTools = []string{"status"}
	}
	b := &bridge{cfg: cfg, out: out, pending: map[string]bool{}}
	conn, err := cfg.Dial(cfg.Socket)
	switch {
	case err == nil:
		b.attach(conn)
	case !NoDaemon(err):
		return fmt.Errorf("connect to the fennec-cua daemon at %s: %w (not starting another daemon)", cfg.Socket, err)
	case cfg.Embedded:
		return fmt.Errorf("%s: %w", cfg.Socket, ErrEmbeddedNoDaemon)
	}
	if cfg.Local != nil {
		b.local = &server{h: cfg.Local, info: cfg.Info, running: map[string]context.CancelFunc{}}
		b.local.sess.AgentID = agentID(cfg.Info.IDPrefix, "", cfg.Info.FixedID)
	}
	br := bufio.NewReaderSize(in, 64*1024)
	var wg sync.WaitGroup
	for {
		raw, tooLong, rerr := readLine(br, MaxMessage)
		if tooLong {
			b.reply(fail(nil, CodeInvalidRequest, "message refused: it exceeds the 64 MiB limit").reply)
		} else if line := bytes.TrimSpace(raw); len(line) > 0 {
			b.handle(ctx, line, &wg)
		}
		if rerr != nil {
			break
		}
	}
	wg.Wait()
	b.mu.Lock()
	c, done := b.conn, b.readerDone
	b.mu.Unlock()
	if c != nil {
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = c.Close()
		}
		select {
		case <-done:
		case <-ctx.Done():
		}
		_ = c.Close()
	}
	return nil
}

type bridge struct {
	cfg   BridgeConfig
	local *server

	wmu sync.Mutex
	out io.Writer

	mu         sync.Mutex
	conn       net.Conn
	readerDone chan struct{}
	pending    map[string]bool
	initLine   []byte
	inited     []byte
}

func (b *bridge) reply(r *reply) {
	r.JSONRPC = "2.0"
	data, _ := json.Marshal(r)
	b.writeLine(data)
}

func (b *bridge) writeLine(data []byte) {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	_, _ = b.out.Write(append(append([]byte{}, data...), '\n'))
}

func (b *bridge) handle(ctx context.Context, line []byte, wg *sync.WaitGroup) {
	var m message
	parsed := json.Unmarshal(line, &m) == nil
	notification := len(m.ID) == 0 || string(m.ID) == "null"
	b.mu.Lock()
	connected := b.conn != nil
	b.mu.Unlock()
	if parsed && m.Method == "initialize" && !notification {
		b.initLine = append([]byte{}, line...)
	}
	if parsed && m.Method == "notifications/initialized" {
		b.inited = append([]byte{}, line...)
	}
	if !connected && parsed {
		if b.answersLocally(m, notification) {
			if p := b.local.dispatch(ctx, line, wg); p != nil {
				<-p.done
				if p.reply != nil {
					b.reply(p.reply)
				}
			}
			return
		}
		if notification {
			return // nothing to tell a daemon that does not run
		}
	}
	if !connected {
		if !parsed {
			b.reply(fail(nil, CodeParse, "parse error").reply)
			return
		}
		if err := b.connect(); err != nil {
			b.reply(fail(m.ID, CodeInternal, err.Error()).reply)
			return
		}
	}
	b.mu.Lock()
	conn := b.conn
	if parsed && !notification && m.Method != "" {
		b.pending[string(m.ID)] = true
	}
	if parsed && m.Method == "notifications/cancelled" {
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			delete(b.pending, string(p.RequestID))
		}
	}
	b.mu.Unlock()
	if conn == nil {
		if parsed && !notification {
			b.mu.Lock()
			delete(b.pending, string(m.ID))
			b.mu.Unlock()
			b.reply(fail(m.ID, CodeInternal, "the connection to the fennec-cua daemon closed; the request was not sent").reply)
		}
		return
	}
	if _, err := conn.Write(append(append([]byte{}, line...), '\n')); err != nil {
		_ = conn.Close() // the reader answers what is pending
	}
}

func (b *bridge) answersLocally(m message, notification bool) bool {
	if b.local == nil {
		return false
	}
	switch m.Method {
	case "initialize", "ping", "tools/list":
		return true
	case "tools/call":
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(m.Params, &p)
		for _, t := range b.cfg.LocalTools {
			if t == p.Name {
				return true
			}
		}
	}
	return notification && m.Method != "notifications/cancelled"
}

// connect reaches the daemon, starting it when nothing listens, and replays
// the client's handshake on the new connection.
func (b *bridge) connect() error {
	conn, err := b.cfg.Dial(b.cfg.Socket)
	if err != nil {
		if !NoDaemon(err) {
			return fmt.Errorf("connect to the fennec-cua daemon at %s: %w (not starting another daemon)", b.cfg.Socket, err)
		}
		if b.cfg.Embedded {
			return fmt.Errorf("%s: %w", b.cfg.Socket, ErrEmbeddedNoDaemon)
		}
		if conn, err = b.start(); err != nil {
			return err
		}
	}
	if b.initLine != nil {
		var m map[string]json.RawMessage
		if json.Unmarshal(b.initLine, &m) == nil {
			m["id"] = json.RawMessage(bridgeInitID)
			data, _ := json.Marshal(m)
			_, err = conn.Write(append(data, '\n'))
		}
		if err == nil && b.inited != nil {
			_, err = conn.Write(append(append([]byte{}, b.inited...), '\n'))
		}
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("the daemon connection closed during the handshake: %w", err)
		}
	}
	b.attach(conn)
	return nil
}

func (b *bridge) start() (net.Conn, error) {
	if b.cfg.Start == nil {
		return nil, fmt.Errorf("no fennec-cua daemon answers at %s", b.cfg.Socket)
	}
	exited, err := b.cfg.Start()
	if err != nil {
		return nil, fmt.Errorf("start the fennec-cua daemon: %w", err)
	}
	deadline := time.Now().Add(b.cfg.StartWait)
	for {
		conn, err := b.cfg.Dial(b.cfg.Socket)
		if err == nil {
			return conn, nil
		}
		if !NoDaemon(err) {
			return nil, fmt.Errorf("connect to the fennec-cua daemon at %s: %w", b.cfg.Socket, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the fennec-cua daemon did not answer at %s within %s", b.cfg.Socket, b.cfg.StartWait)
		}
		select {
		case err := <-exited:
			if err == nil {
				err = errors.New("exit status 0")
			}
			return nil, fmt.Errorf("the fennec-cua daemon exited before answering (%v); run `fennec-cua serve` to see why", err)
		case <-time.After(b.cfg.Poll):
		}
	}
}

func (b *bridge) attach(conn net.Conn) {
	done := make(chan struct{})
	b.mu.Lock()
	b.conn, b.readerDone = conn, done
	b.mu.Unlock()
	go func() {
		defer close(done)
		r := bufio.NewReaderSize(conn, 64*1024)
		for {
			raw, tooLong, err := readLine(r, MaxMessage)
			if line := bytes.TrimSpace(raw); !tooLong && len(line) > 0 {
				var m message
				if json.Unmarshal(line, &m) == nil && string(m.ID) == bridgeInitID {
					// The replayed handshake's answer is the bridge's own.
				} else {
					b.mu.Lock()
					delete(b.pending, string(m.ID))
					b.mu.Unlock()
					b.writeLine(line)
				}
			}
			if err != nil {
				break
			}
		}
		b.dropped(conn)
	}()
}

// dropped answers every request still waiting on conn with an error and
// forgets the connection; a later request reconnects.
func (b *bridge) dropped(conn net.Conn) {
	b.mu.Lock()
	if b.conn != conn {
		b.mu.Unlock()
		return
	}
	b.conn = nil
	ids := b.pending
	b.pending = map[string]bool{}
	b.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	for id := range ids {
		b.reply(&reply{ID: json.RawMessage(id), Error: &rpcError{Code: CodeInternal, Message: "the connection to the fennec-cua daemon closed before it answered; the call may have taken effect, so it was not sent again: observe before retrying"}})
	}
}
