// Package ego drives ego lite (com.citrolabs.ego.lite, a Chromium browser
// built for agents) for the REPL's browser API (docs/codex-parity.md,
// section 8). One bridge script per REPL session runs in ego's own Node
// runtime through its public SDK and connects back to a unix socket this
// package listens on; every decision (tab registry, element ids, the state
// text, locators, key names, safety checks) is made here in Go.
package ego

import (
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed bridge.js
var bridgeJS string

// Bridge limits.
const (
	// ConnectTimeout bounds a bridge's start: the process runs and dials
	// back with its token.
	ConnectTimeout = 15 * time.Second
	// CallTimeout bounds one request unless the caller's context ends
	// sooner.
	CallTimeout = 45 * time.Second
)

// ErrBridgeGone means the bridge process ended while a request was in
// flight; the next request starts a new one.
var ErrBridgeGone = errors.New("the ego lite bridge stopped")

// RemoteError is an error the SDK raised inside the bridge.
type RemoteError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

func (e *RemoteError) Error() string { return e.Message }

// Proc is a started bridge process.
type Proc interface {
	Kill()
	// Done is closed when the process has exited.
	Done() <-chan struct{}
}

// Starter starts a bridge: script is the complete bridge source (with SOCK
// and TOKEN defined), which a real starter hands to `ego-browser nodejs
// -e`. Tests start an in-process fake that dials sock and presents token.
type Starter func(ctx context.Context, sock, token, script string) (Proc, error)

// ExecStarter runs bin (ego-browser) with the script.
func ExecStarter(bin string, log func(string, ...any)) Starter {
	return func(ctx context.Context, sock, token, script string) (Proc, error) {
		cmd := exec.Command(bin, "nodejs", "-e", script)
		cmd.Stdin = nil
		tail := &tailWriter{max: 4096}
		cmd.Stdout, cmd.Stderr = tail, tail
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		p := &execProc{cmd: cmd, done: make(chan struct{})}
		go func() {
			err := cmd.Wait()
			if log != nil && err != nil {
				log("ego bridge exited: %v; %s", err, strings.TrimSpace(tail.String()))
			}
			close(p.done)
		}()
		return p, nil
	}
}

type execProc struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *execProc) Kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (p *execProc) Done() <-chan struct{} { return p.done }

// tailWriter keeps the last bytes a bridge printed, for the log.
type tailWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// Client is one REPL session's bridge: started on the first request,
// started again after a crash, closed when the session ends.
type Client struct {
	// Dir holds the socket; keep it short (macOS allows 104 bytes).
	Dir   string
	Start Starter
	Log   func(string, ...any)
	// Timeout bounds a request; zero means CallTimeout.
	Timeout time.Duration

	mu       sync.Mutex
	conn     *bridgeConn
	gen      int // counts started bridges
	closed   bool
	starting chan struct{} // closed when a start in progress ends
}

// bridgeConn is one live bridge.
type bridgeConn struct {
	conn net.Conn
	proc Proc
	gen  int

	wmu     sync.Mutex
	mu      sync.Mutex
	next    int64
	pending map[int64]chan reply
	dead    chan struct{}
	err     error
}

type reply struct {
	ID    int64           `json:"id"`
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value"`
	Error *RemoteError    `json:"error"`
}

// Gen is the number of bridges started so far: a request answered under
// another generation than the caller's last means the bridge restarted and
// SDK handles (task spaces) must be opened again.
func (c *Client) Gen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// Live says whether a bridge is running.
func (c *Client) Live() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil && !c.conn.isDead()
}

// Do sends one request and waits for its reply. It starts the bridge when
// none runs. gen is the generation that answered.
func (c *Client) Do(ctx context.Context, op string, args any) (value json.RawMessage, gen int, err error) {
	bc, err := c.ensure(ctx)
	if err != nil {
		return nil, 0, err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = CallTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	v, err := bc.call(cctx, op, args)
	return v, bc.gen, err
}

// Close ends the bridge: a polite goodbye, then the process is killed if
// it lingers.
func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	bc := c.conn
	c.conn = nil
	c.mu.Unlock()
	if bc != nil {
		bc.shutdown()
	}
}

func (c *Client) ensure(ctx context.Context) (*bridgeConn, error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, errors.New("the browser session is closed")
		}
		if c.conn != nil && !c.conn.isDead() {
			bc := c.conn
			c.mu.Unlock()
			return bc, nil
		}
		if c.starting != nil {
			wait := c.starting
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		c.starting = make(chan struct{})
		c.gen++
		gen := c.gen
		c.mu.Unlock()
		bc, err := c.start(ctx, gen)
		c.mu.Lock()
		close(c.starting)
		c.starting = nil
		if err == nil {
			if c.closed {
				c.mu.Unlock()
				bc.shutdown()
				return nil, errors.New("the browser session is closed")
			}
			c.conn = bc
		}
		c.mu.Unlock()
		return bc, err
	}
}

// start listens on a fresh socket, starts the bridge and waits for it to
// dial back with the token.
func (c *Client) start(ctx context.Context, gen int) (*bridgeConn, error) {
	if c.Start == nil {
		return nil, errors.New("ego lite is not available")
	}
	dir := c.Dir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, fmt.Sprintf("eb%s.sock", randHex(4)))
	if len(sock) > 100 {
		return nil, fmt.Errorf("the bridge socket path is too long for macOS: %s", sock)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	defer func() {
		ln.Close()
		os.Remove(sock)
	}()
	_ = os.Chmod(sock, 0o600)
	token := randHex(16)
	script := "const SOCK = " + jsString(sock) + ";\nconst TOKEN = " + jsString(token) + ";\n" + bridgeJS
	proc, err := c.Start(ctx, sock, token, script)
	if err != nil {
		return nil, fmt.Errorf("start the ego lite bridge: %w", err)
	}
	type accepted struct {
		conn net.Conn
		rd   *bufio.Reader
		err  error
	}
	got := make(chan accepted, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				got <- accepted{err: err}
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(ConnectTimeout))
			rd := bufio.NewReaderSize(conn, 1<<16)
			line, err := rd.ReadBytes('\n')
			var hello struct {
				Hello string `json:"hello"`
			}
			if err == nil && json.Unmarshal(line, &hello) == nil && hello.Hello == token {
				_ = conn.SetReadDeadline(time.Time{})
				got <- accepted{conn: conn, rd: rd}
				return
			}
			// Not our bridge: refuse it and keep listening.
			conn.Close()
		}
	}()
	timer := time.NewTimer(ConnectTimeout)
	defer timer.Stop()
	select {
	case a := <-got:
		if a.err != nil {
			proc.Kill()
			return nil, a.err
		}
		bc := &bridgeConn{conn: a.conn, proc: proc, gen: gen, pending: map[int64]chan reply{}, dead: make(chan struct{})}
		go bc.read(a.rd, c.Log)
		go func() {
			select {
			case <-proc.Done():
				bc.fail(ErrBridgeGone)
			case <-bc.dead:
			}
		}()
		return bc, nil
	case <-proc.Done():
		return nil, errors.New("the ego lite bridge exited before it connected; is ego lite installed and signed in?")
	case <-timer.C:
		proc.Kill()
		return nil, errors.New("the ego lite bridge did not connect in time")
	case <-ctx.Done():
		proc.Kill()
		return nil, ctx.Err()
	}
}

func (bc *bridgeConn) isDead() bool {
	select {
	case <-bc.dead:
		return true
	default:
		return false
	}
}

func (bc *bridgeConn) read(rd *bufio.Reader, log func(string, ...any)) {
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 {
			var r reply
			if json.Unmarshal(line, &r) == nil {
				bc.mu.Lock()
				ch := bc.pending[r.ID]
				delete(bc.pending, r.ID)
				bc.mu.Unlock()
				if ch != nil {
					ch <- r
				}
			} else if log != nil {
				log("ego bridge: unreadable reply")
			}
		}
		if err != nil {
			bc.fail(ErrBridgeGone)
			return
		}
	}
}

// fail ends the connection and every request waiting on it.
func (bc *bridgeConn) fail(err error) {
	bc.mu.Lock()
	select {
	case <-bc.dead:
		bc.mu.Unlock()
		return
	default:
	}
	bc.err = err
	close(bc.dead)
	bc.pending = map[int64]chan reply{}
	bc.mu.Unlock()
	bc.conn.Close()
	// A bridge whose socket closed exits by itself; one that lingers is
	// killed.
	go func() {
		select {
		case <-bc.proc.Done():
		case <-time.After(2 * time.Second):
			bc.proc.Kill()
		}
	}()
}

func (bc *bridgeConn) shutdown() {
	bc.wmu.Lock()
	_ = bc.conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = bc.conn.Write([]byte(`{"op":"bye"}` + "\n"))
	bc.wmu.Unlock()
	select {
	case <-bc.proc.Done():
	case <-time.After(2 * time.Second):
	}
	bc.fail(errors.New("the browser session is closed"))
}

func (bc *bridgeConn) call(ctx context.Context, op string, args any) (json.RawMessage, error) {
	ch := make(chan reply, 1)
	bc.mu.Lock()
	if bc.isDeadLocked() {
		bc.mu.Unlock()
		return nil, ErrBridgeGone
	}
	bc.next++
	id := bc.next
	bc.pending[id] = ch
	bc.mu.Unlock()
	data, err := json.Marshal(map[string]any{"id": id, "op": op, "args": args})
	if err != nil {
		bc.drop(id)
		return nil, err
	}
	bc.wmu.Lock()
	_ = bc.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = bc.conn.Write(append(data, '\n'))
	bc.wmu.Unlock()
	if err != nil {
		bc.drop(id)
		bc.fail(ErrBridgeGone)
		return nil, ErrBridgeGone
	}
	select {
	case r := <-ch:
		if !r.OK {
			if r.Error == nil {
				r.Error = &RemoteError{Message: "the request failed"}
			}
			return nil, r.Error
		}
		return r.Value, nil
	case <-bc.dead:
		return nil, ErrBridgeGone
	case <-ctx.Done():
		// The reply, if it comes, is dropped.
		bc.drop(id)
		return nil, ctx.Err()
	}
}

func (bc *bridgeConn) isDeadLocked() bool {
	select {
	case <-bc.dead:
		return true
	default:
		return false
	}
}

func (bc *bridgeConn) drop(id int64) {
	bc.mu.Lock()
	delete(bc.pending, id)
	bc.mu.Unlock()
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// jsString quotes s as a JavaScript string literal.
func jsString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}
