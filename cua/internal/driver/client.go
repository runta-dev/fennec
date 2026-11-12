// Package driver talks to the bundled cua-driver (MIT) over its unix socket:
// line-delimited JSON, {"method":"call","name":tool,"args":{...}} answered by
// {"ok":true,"result":...} or {"ok":false,"error":"..."}. A connection is a
// driver session, so each lane (input, capture, one per vision agent) keeps
// its own persistent connection and sends one request at a time on it.
package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Lanes the desk uses. Capture runs apart from input so a capture never
// disturbs input state.
const (
	LaneInput   = "input"
	LaneCapture = "capture"
)

// Timing.
const (
	ConnectRetry   = 250 * time.Millisecond
	ConnectWait    = 10 * time.Second
	RequestTimeout = 30 * time.Second
	// PerTypedUnit extends a type_text call's deadline per UTF-16 unit.
	PerTypedUnit = 40 * time.Millisecond
)

// sessionEnded is the driver's exact answer for a session it dropped.
const sessionEnded = "session has ended"

// Error is a driver call failure.
type Error struct {
	Tool    string
	Message string
	// Result is the driver's structured reply, when it sent one with the error.
	Result json.RawMessage
	// Transport is set when the failure happened before the driver answered.
	Transport bool
}

func (e *Error) Error() string { return e.Tool + ": " + e.Message }

// Client is a cua-driver client.
type Client struct {
	Socket string
	// Dial overrides how a connection is made (tests).
	Dial func(ctx context.Context, socket string) (net.Conn, error)

	mu    sync.Mutex
	lanes map[string]*lane
}

type lane struct {
	mu   sync.Mutex
	conn net.Conn
	r    *bufio.Reader
}

type request struct {
	Method string `json:"method"`
	Name   string `json:"name,omitempty"`
	Args   any    `json:"args,omitempty"`
}

type response struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// TypeTimeout is the deadline a type_text call of text needs.
func TypeTimeout(text string) time.Duration {
	units := 0
	for _, r := range text {
		units++
		if r >= 0x10000 {
			units++
		}
	}
	return RequestTimeout + time.Duration(units)*PerTypedUnit
}

// Call runs tool on lane. When the driver answers that the lane's session
// ended, the lane reconnects (a new session) and the call is sent once more.
func (c *Client) Call(ctx context.Context, laneName, tool string, args any) (json.RawMessage, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, RequestTimeout)
		defer cancel()
	}
	l := c.lane(laneName)
	l.mu.Lock()
	defer l.mu.Unlock()
	res, err := c.roundTrip(ctx, l, tool, args)
	var de *Error
	if errors.As(err, &de) && !de.Transport && strings.TrimSpace(de.Message) == sessionEnded {
		l.close()
		res, err = c.roundTrip(ctx, l, tool, args)
	}
	return res, err
}

// Close drops every lane's connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.lanes {
		l.mu.Lock()
		l.close()
		l.mu.Unlock()
	}
}

// EndLane drops one lane, ending its driver session.
func (c *Client) EndLane(name string) {
	c.mu.Lock()
	l := c.lanes[name]
	delete(c.lanes, name)
	c.mu.Unlock()
	if l != nil {
		l.mu.Lock()
		l.close()
		l.mu.Unlock()
	}
}

func (c *Client) lane(name string) *lane {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lanes == nil {
		c.lanes = map[string]*lane{}
	}
	l := c.lanes[name]
	if l == nil {
		l = &lane{}
		c.lanes[name] = l
	}
	return l
}

func (l *lane) close() {
	if l.conn != nil {
		_ = l.conn.Close()
	}
	l.conn, l.r = nil, nil
}

// roundTrip must run with l.mu held. A connection that fails before a reply
// is replaced; the request is resent only when it was never written, so a
// call that may have reached the driver is never sent twice.
func (c *Client) roundTrip(ctx context.Context, l *lane, tool string, args any) (json.RawMessage, error) {
	if l.conn == nil {
		if err := c.connect(ctx, l); err != nil {
			return nil, &Error{Tool: tool, Message: err.Error(), Transport: true}
		}
	}
	line, err := json.Marshal(request{Method: "call", Name: tool, Args: args})
	if err != nil {
		return nil, &Error{Tool: tool, Message: err.Error(), Transport: true}
	}
	deadline, _ := ctx.Deadline()
	// arm makes ctx's end interrupt blocking I/O on the current connection.
	arm := func() func() bool {
		conn := l.conn
		_ = conn.SetDeadline(deadline)
		return context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	}
	stop := arm()
	if _, err := l.conn.Write(append(line, '\n')); err != nil {
		stop()
		l.close()
		// Nothing reached the driver: reconnect once and send.
		if cerr := c.connect(ctx, l); cerr != nil {
			return nil, &Error{Tool: tool, Message: "send: " + err.Error(), Transport: true}
		}
		stop = arm()
		if _, err := l.conn.Write(append(line, '\n')); err != nil {
			stop()
			l.close()
			return nil, &Error{Tool: tool, Message: "send: " + err.Error(), Transport: true}
		}
	}
	defer func() { stop() }()
	raw, err := l.r.ReadBytes('\n')
	if err != nil {
		l.close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Tool: tool, Message: "no reply (the input may have been sent): " + err.Error(), Transport: true}
	}
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, &Error{Tool: tool, Message: "undecodable reply: " + err.Error()}
	}
	if !resp.OK {
		msg := resp.Error
		if msg == "" {
			msg = "failed without a reason"
		}
		return nil, &Error{Tool: tool, Message: msg, Result: resp.Result}
	}
	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil, &Error{Tool: tool, Message: "empty reply"}
	}
	return resp.Result, nil
}

// connect dials, retrying every ConnectRetry for up to ConnectWait while the
// driver (re)starts.
func (c *Client) connect(ctx context.Context, l *lane) error {
	dial := c.Dial
	if dial == nil {
		dial = func(ctx context.Context, socket string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}
	}
	give := time.Now().Add(ConnectWait)
	for {
		conn, err := dial(ctx, c.Socket)
		if err == nil {
			l.conn, l.r = conn, bufio.NewReaderSize(conn, 64*1024)
			return nil
		}
		if time.Now().Add(ConnectRetry).After(give) {
			return fmt.Errorf("connect cua-driver at %s: %w", c.Socket, err)
		}
		select {
		case <-time.After(ConnectRetry):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
