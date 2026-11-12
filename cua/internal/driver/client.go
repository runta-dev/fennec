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
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/protocol"
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
	// ReadRetry is the pause before a read that got an empty reply is sent
	// once more.
	ReadRetry = 250 * time.Millisecond
	// ClickSpacing separates two near single clicks, measured from the end
	// of the earlier one, so the app never reads them as a double click.
	ClickSpacing = 500 * time.Millisecond
	// ClickDistance is how near (in the click's own coordinates) two clicks
	// must be to be spaced.
	ClickDistance = 16.0
)

// sessionEnded starts the driver's own answer for a session it dropped
// ("session has ended; tool call 'x' was rejected. ...").
const sessionEnded = "session has ended"

// Driver refusal codes worth naming.
const (
	CodeDisplayAsleep = "display_asleep"
	CodeScreenLocked  = "screen_locked"
	// ReasonNeverRead is the driver's reason when a window-pixel action
	// names a window it has no snapshot of.
	ReasonNeverRead = "window_never_read"
)

// inputTools produce input on the desktop.
var inputTools = map[string]bool{
	"click": true, "double_click": true, "right_click": true, "drag": true, "scroll": true,
	"move_cursor": true, "type_text": true, "press_key": true, "hotkey": true,
}

// readTools only read; an empty reply to one is safe to ask again.
var readTools = map[string]bool{
	"list_windows": true, "get_window_state": true, "get_desktop_state": true, "end_session": true,
	"get_screen_size": true, "list_apps": true, "get_cursor_position": true,
}

// IsInput reports whether tool produces desktop input.
func IsInput(tool string) bool { return inputTools[tool] }

// IsRead reports whether tool only reads, so an empty reply may be retried.
func IsRead(tool string) bool { return readTools[tool] }

// Error is a driver call failure.
type Error struct {
	Tool    string
	Message string
	// Result is the driver's structured reply, when it sent one with the error.
	Result json.RawMessage
	// Transport is set when the failure happened before the driver answered.
	Transport bool
	// Code is the driver's structured refusal or error code, when given
	// (such as display_asleep or screen_locked).
	Code string
	// Reason is the driver's structured reason, when given.
	Reason string
	// ActualFront is the window the driver found in front when bringing a
	// window forward failed; zero when not reported.
	ActualFront uint64
	// Refused is set when nothing was sent: the driver or fennec-cua
	// refused before any input.
	Refused bool
}

func (e *Error) Error() string { return e.Tool + ": " + e.Message }

// Locked reports a failure caused by the locked screen.
func (e *Error) Locked() bool {
	return e.Code == CodeScreenLocked || e.Reason == CodeScreenLocked || strings.Contains(e.Message, "the screen is locked")
}

// lockedNote explains a locked-screen failure instead of leaving it obscure.
const lockedNote = "; the screen is locked: macOS routes input to the lock screen, so nothing reached the target; unlock the Mac, then try again"

// Activity yields foreground input to a busy user; policy.Activity
// implements it.
type Activity interface {
	// Check refuses with nothing sent while the user is using the mouse or
	// keyboard.
	Check() *protocol.Error
	// Own records that fennec-cua's own foreground input just ended.
	Own()
}

// Client is a cua-driver client.
type Client struct {
	Socket string
	// Dial overrides how a connection is made (tests).
	Dial func(ctx context.Context, socket string) (net.Conn, error)
	// ConnectWait bounds how long a call waits for a (re)starting driver;
	// zero means ConnectWait.
	ConnectWait time.Duration
	// Activity, when set, refuses foreground input and bringing a window
	// to the front while the user is busy, and is told of fennec-cua's own
	// foreground input. Background input is neither checked nor own.
	Activity Activity
	// ToolErrors, when set, turns a result the driver marks isError into an
	// *Error (see Check). Off, such results are returned as they are.
	ToolErrors bool

	mu    sync.Mutex
	lanes map[string]*lane

	clickOnce sync.Once
	clickSem  chan struct{} // held while a spaced click runs
	clickMu   sync.Mutex
	lastClick *clickSpot
	lastEnd   time.Time

	windows WindowStates
}

type clickSpot struct {
	x, y   float64
	target string
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

// CallLimit is a call's time limit: RequestTimeout, plus PerTypedUnit per
// UTF-16 unit of the text for type_text.
func CallLimit(tool string, args any) time.Duration {
	if tool != "type_text" {
		return RequestTimeout
	}
	text, _ := argMap(args)["text"].(string)
	return TypeTimeout(text)
}

// argMap views args as a JSON object; nil when it is not one.
func argMap(args any) map[string]any {
	switch a := args.(type) {
	case nil:
		return nil
	case map[string]any:
		return a
	}
	data, err := json.Marshal(args)
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return m
}

// foreground reports input the driver delivers in the foreground.
func foreground(tool string, m map[string]any) bool {
	if !inputTools[tool] {
		return false
	}
	mode, _ := m["delivery_mode"].(string)
	return mode != "background"
}

// Call runs tool on lane. When the driver answers that the lane's session
// ended, the lane reconnects (a new session) and the call is sent once more.
// A read that gets an empty reply is sent once more after ReadRetry; input
// never is. Near single clicks are spaced by ClickSpacing.
func (c *Client) Call(ctx context.Context, laneName, tool string, args any) (json.RawMessage, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, CallLimit(tool, args))
		defer cancel()
	}
	m := argMap(args)
	fg := foreground(tool, m)
	if c.Activity != nil && (fg || tool == "bring_to_front") {
		if perr := c.Activity.Check(); perr != nil {
			msg := perr.Message
			if perr.Hint != "" {
				msg += "; " + perr.Hint
			}
			return nil, &Error{Tool: tool, Message: msg, Code: string(perr.Code), Refused: true}
		}
	}
	if spot, ok := clickPoint(tool, m); ok {
		done, err := c.spaceClick(ctx, spot)
		if err != nil {
			return nil, err
		}
		defer done()
	}
	res, err := c.send(ctx, laneName, tool, args)
	if fg && c.Activity != nil && !refusedBeforeSending(err) {
		c.Activity.Own()
	}
	if err == nil && c.ToolErrors {
		if cerr := Check(tool, res); cerr != nil {
			return nil, cerr
		}
	}
	return res, err
}

func refusedBeforeSending(err error) bool {
	var de *Error
	return errors.As(err, &de) && de.Refused
}

func (c *Client) send(ctx context.Context, laneName, tool string, args any) (json.RawMessage, error) {
	l := c.lane(laneName)
	l.mu.Lock()
	defer l.mu.Unlock()
	res, err := c.roundTrip(ctx, l, tool, args)
	var de *Error
	if errors.As(err, &de) && !de.Transport && strings.HasPrefix(strings.TrimSpace(de.Message), sessionEnded) {
		l.close()
		return c.roundTrip(ctx, l, tool, args)
	}
	if readTools[tool] && emptyReply(err) {
		select {
		case <-time.After(ReadRetry):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		l.close()
		return c.roundTrip(ctx, l, tool, args)
	}
	return res, err
}

// emptyReply is a driver that closed without answering or answered nothing.
func emptyReply(err error) bool {
	var de *Error
	if !errors.As(err, &de) {
		return false
	}
	return de.Message == "empty reply" || (de.Transport && strings.HasPrefix(de.Message, "no reply"))
}

// clickPoint is a single click by coordinates.
func clickPoint(tool string, m map[string]any) (clickSpot, bool) {
	if tool != "click" || m == nil {
		return clickSpot{}, false
	}
	x, ok1 := m["x"].(float64)
	y, ok2 := m["y"].(float64)
	if !ok1 || !ok2 {
		if xi, ok := m["x"].(int); ok {
			x, ok1 = float64(xi), true
		}
		if yi, ok := m["y"].(int); ok {
			y, ok2 = float64(yi), true
		}
	}
	if !ok1 || !ok2 {
		return clickSpot{}, false
	}
	if n, ok := m["count"]; ok && fmt.Sprint(n) != "1" {
		return clickSpot{}, false
	}
	target := fmt.Sprint(m["pid"], "/", m["window_id"])
	return clickSpot{x: x, y: y, target: target}, true
}

// spaceClick waits for any click in flight, then until ClickSpacing has
// passed since the end of a near previous click. The returned done marks the
// click's end and lets the next one through.
func (c *Client) spaceClick(ctx context.Context, spot clickSpot) (func(), error) {
	c.clickOnce.Do(func() { c.clickSem = make(chan struct{}, 1) })
	select {
	case c.clickSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.clickMu.Lock()
	var wait time.Duration
	if last := c.lastClick; last != nil && last.target == spot.target && math.Hypot(spot.x-last.x, spot.y-last.y) <= ClickDistance {
		wait = ClickSpacing - time.Since(c.lastEnd)
	}
	c.clickMu.Unlock()
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			<-c.clickSem
			return nil, ctx.Err()
		}
	}
	mark := func() {
		c.clickMu.Lock()
		s := spot
		c.lastClick, c.lastEnd = &s, time.Now()
		c.clickMu.Unlock()
	}
	mark() // a click cancelled while running still counts from its end
	var once sync.Once
	return func() {
		once.Do(func() {
			mark()
			<-c.clickSem
		})
	}, nil
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
			return nil, &Error{Tool: tool, Message: err.Error(), Transport: true, Refused: true}
		}
	}
	line, err := json.Marshal(request{Method: "call", Name: tool, Args: args})
	if err != nil {
		return nil, &Error{Tool: tool, Message: err.Error(), Transport: true, Refused: true}
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
			return nil, &Error{Tool: tool, Message: "send: " + err.Error(), Transport: true, Refused: true}
		}
		stop = arm()
		if _, err := l.conn.Write(append(line, '\n')); err != nil {
			stop()
			l.close()
			return nil, &Error{Tool: tool, Message: "send: " + err.Error(), Transport: true, Refused: true}
		}
	}
	defer func() { stop() }()
	raw, err := l.r.ReadBytes('\n')
	if err != nil {
		l.close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			// The connection deadline can fire just before the context.
			return nil, context.DeadlineExceeded
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
		e := &Error{Tool: tool, Message: msg, Result: resp.Result}
		annotate(e, resp.Result)
		if e.Locked() && !strings.Contains(e.Message, lockedNote) {
			e.Message += lockedNote
		}
		return nil, e
	}
	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil, &Error{Tool: tool, Message: "empty reply"}
	}
	return resp.Result, nil
}

// toolResult is the MCP-shaped part of a driver result.
type toolResult struct {
	IsError bool `json:"isError"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Structured json.RawMessage `json:"structuredContent"`
}

type structured struct {
	Status      string  `json:"status"`
	Code        string  `json:"code"`
	Reason      string  `json:"reason"`
	ActualFront *uint64 `json:"actual_foreground_window_id"`
	Refusal     *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"refusal"`
}

// annotate fills e's code, reason and foreground window from a result.
func annotate(e *Error, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var r toolResult
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	var s structured
	src := r.Structured
	if len(src) == 0 {
		src = raw
	}
	if json.Unmarshal(src, &s) != nil {
		return
	}
	e.Code, e.Reason = s.Code, s.Reason
	if s.Refusal != nil {
		if s.Refusal.Code != "" {
			e.Code = s.Refusal.Code
		}
		e.Refused = true
	}
	if s.Status == "refused" {
		e.Refused = true
	}
	if s.ActualFront != nil {
		e.ActualFront = *s.ActualFront
	}
}

// Check turns a result the driver marks isError into an *Error prefixed
// with the tool: its text, after its structured reason when given, and the
// window actually in front when activation failed. A display that stays
// asleep or a locked screen is a refusal saying so. Other results are nil.
func Check(tool string, result json.RawMessage) error {
	var r toolResult
	if err := json.Unmarshal(result, &r); err != nil {
		return &Error{Tool: tool, Message: "undecodable reply: " + err.Error()}
	}
	if !r.IsError {
		return nil
	}
	var texts []string
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			texts = append(texts, c.Text)
		}
	}
	text := strings.Join(texts, "\n")
	if text == "" {
		text = "failed without a reason"
	}
	e := &Error{Tool: tool, Result: result}
	annotate(e, result)
	msg := text
	if e.Reason != "" {
		msg = e.Reason + ": " + text
	}
	if e.ActualFront != 0 {
		msg += fmt.Sprintf("; actual foreground window %d", e.ActualFront)
	}
	if e.Code == CodeDisplayAsleep {
		e.Refused = true
		if !strings.Contains(msg, CodeDisplayAsleep) {
			msg = CodeDisplayAsleep + ": " + msg
		}
		msg += "; the display is asleep and did not wake, so nothing was captured"
	}
	e.Message = msg
	if e.Locked() {
		e.Message += lockedNote
	}
	return e
}

// connect dials, retrying every ConnectRetry for up to the connect wait while
// the driver (re)starts.
func (c *Client) connect(ctx context.Context, l *lane) error {
	dial := c.Dial
	if dial == nil {
		dial = func(ctx context.Context, socket string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}
	}
	wait := c.ConnectWait
	if wait <= 0 {
		wait = ConnectWait
	}
	give := time.Now().Add(wait)
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
