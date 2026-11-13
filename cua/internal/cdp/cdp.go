// Package cdp is a small Chrome DevTools Protocol client for one browser
// connection: JSON messages over a byte stream, each ended by a NUL byte,
// as Chromium speaks on --remote-debugging-pipe. Commands to a page go
// through flattened target sessions (Target.attachToTarget with flatten):
// the session id rides on each message.
package cdp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrClosed means the connection ended; a command in flight may have
// reached the browser.
var ErrClosed = errors.New("the browser connection closed")

// MaxMessage bounds one incoming message (an accessibility tree of a large
// page or a full-page screenshot fits comfortably).
const MaxMessage = 256 << 20

// Error is an error the browser answered.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e.Data != "" {
		return e.Message + ": " + e.Data
	}
	return e.Message
}

// Event is a message the browser sent on its own.
type Event struct {
	Method    string
	SessionID string
	Params    json.RawMessage
}

type message struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}

// Conn is one browser connection.
type Conn struct {
	w     io.Writer
	close func()

	wmu sync.Mutex

	mu      sync.Mutex
	next    int64
	pending map[int64]chan message
	subs    map[int]func(Event)
	nextSub int
	dead    chan struct{}
	err     error
}

// New starts reading r. w carries commands; close (optional) releases the
// transport when the connection ends.
func New(r io.Reader, w io.Writer, close func()) *Conn {
	c := &Conn{w: w, close: close, pending: map[int64]chan message{}, subs: map[int]func(Event){}, dead: make(chan struct{})}
	go c.read(r)
	return c
}

// Done is closed when the connection has ended.
func (c *Conn) Done() <-chan struct{} { return c.dead }

// Err is why the connection ended, or nil while it is live.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection.
func (c *Conn) Close() { c.fail(ErrClosed) }

// Subscribe calls fn for every event, on the reading goroutine: fn must
// not block or call the connection. The returned function unsubscribes.
func (c *Conn) Subscribe(fn func(Event)) (cancel func()) {
	c.mu.Lock()
	c.nextSub++
	id := c.nextSub
	c.subs[id] = fn
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.subs, id)
		c.mu.Unlock()
	}
}

func (c *Conn) read(r io.Reader) {
	rd := bufio.NewReaderSize(r, 1<<16)
	for {
		data, err := readFrame(rd)
		if len(data) > 0 {
			var m message
			if json.Unmarshal(data, &m) == nil {
				c.dispatch(m)
			}
		}
		if err != nil {
			c.fail(ErrClosed)
			return
		}
	}
}

// readFrame reads up to the next NUL byte.
func readFrame(rd *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := rd.ReadSlice(0)
		buf = append(buf, chunk...)
		if len(buf) > MaxMessage {
			return nil, fmt.Errorf("a browser message exceeds %d bytes", MaxMessage)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, err
		}
		return buf[:len(buf)-1], nil
	}
}

func (c *Conn) dispatch(m message) {
	if m.ID != 0 {
		c.mu.Lock()
		ch := c.pending[m.ID]
		delete(c.pending, m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
		return
	}
	if m.Method == "" {
		return
	}
	ev := Event{Method: m.Method, SessionID: m.SessionID, Params: m.Params}
	c.mu.Lock()
	subs := make([]func(Event), 0, len(c.subs))
	for _, fn := range c.subs {
		subs = append(subs, fn)
	}
	c.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	select {
	case <-c.dead:
		c.mu.Unlock()
		return
	default:
	}
	c.err = err
	close(c.dead)
	c.pending = map[int64]chan message{}
	c.mu.Unlock()
	if c.close != nil {
		c.close()
	}
}

// Call sends a command (to the browser when session is empty, else to
// that target session) and waits for its result. A context that ends
// first drops the reply.
func (c *Conn) Call(ctx context.Context, session, method string, params any) (json.RawMessage, error) {
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = data
	} else {
		raw = json.RawMessage("{}")
	}
	ch := make(chan message, 1)
	c.mu.Lock()
	select {
	case <-c.dead:
		c.mu.Unlock()
		return nil, ErrClosed
	default:
	}
	c.next++
	id := c.next
	c.pending[id] = ch
	c.mu.Unlock()
	data, err := json.Marshal(message{ID: id, SessionID: session, Method: method, Params: raw})
	if err != nil {
		c.drop(id)
		return nil, err
	}
	c.wmu.Lock()
	_, err = c.w.Write(append(data, 0))
	c.wmu.Unlock()
	if err != nil {
		c.drop(id)
		c.fail(ErrClosed)
		return nil, ErrClosed
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, m.Error
		}
		if len(m.Result) == 0 {
			return json.RawMessage("{}"), nil
		}
		return m.Result, nil
	case <-c.dead:
		return nil, ErrClosed
	case <-ctx.Done():
		c.drop(id)
		return nil, ctx.Err()
	}
}

func (c *Conn) drop(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}
