package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fennec/cua/internal/protocol"
)

func (f *fakeDriver) sent(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r == name {
			n++
		}
	}
	return n
}

func (f *fakeDriver) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

const ended = `{"ok":false,"error":"session has ended; tool call 'click' was rejected. Call start_session without a session label to start a new unnamed session."}`

func TestTypeLimitPerUTF16Unit(t *testing.T) {
	if got := CallLimit("type_text", map[string]any{"text": strings.Repeat("x", 6000)}); got != 270*time.Second {
		t.Errorf("6000 characters: %s", got)
	}
	if got := CallLimit("type_text", map[string]any{"text": "a\u00e9\U0001F600"}); got != RequestTimeout+4*PerTypedUnit {
		t.Errorf("surrogate pair: %s", got)
	}
	type typeArgs struct {
		Text string `json:"text"`
	}
	if got := CallLimit("type_text", typeArgs{Text: "ab"}); got != RequestTimeout+2*PerTypedUnit {
		t.Errorf("struct args: %s", got)
	}
	for _, tool := range []string{"click", "press_key", "get_window_state"} {
		if got := CallLimit(tool, map[string]any{"text": strings.Repeat("x", 6000)}); got != RequestTimeout {
			t.Errorf("%s: %s", tool, got)
		}
	}
	// A long type is not cut at the base limit: a slow reply well past a
	// short base would still be waited for when the limit is per unit.
	_, sock := newFake(t, func(int32, string, json.RawMessage) string {
		time.Sleep(50 * time.Millisecond)
		return `{"ok":true,"result":{"typed":true}}`
	})
	c := &Client{Socket: sock}
	defer c.Close()
	if _, err := c.Call(context.Background(), LaneInput, "type_text", map[string]any{"text": strings.Repeat("y", 6000)}); err != nil {
		t.Fatal(err)
	}
}

func TestReplyMapping(t *testing.T) {
	_, sock := newFake(t, func(_ int32, name string, _ json.RawMessage) string {
		switch name {
		case "refused":
			return `{"ok":false,"error":"no permission","exit_code":1}`
		case "empty":
			return `{"ok":true}`
		case "junk":
			return `{`
		}
		return `{"ok":true,"result":{"structuredContent":{"windows":[]}}}`
	})
	c := &Client{Socket: sock}
	defer c.Close()
	ctx := context.Background()
	if res, err := c.Call(ctx, LaneInput, "list_windows", nil); err != nil || !strings.Contains(string(res), `"windows":[]`) {
		t.Errorf("result: %s %v", res, err)
	}
	if _, err := c.Call(ctx, LaneInput, "refused", nil); err == nil || err.Error() != "refused: no permission" {
		t.Errorf("refusal: %v", err)
	}
	if _, err := c.Call(ctx, LaneInput, "empty", nil); err == nil || err.Error() != "empty: empty reply" {
		t.Errorf("empty: %v", err)
	}
	if _, err := c.Call(ctx, LaneInput, "junk", nil); err == nil || !strings.HasPrefix(err.Error(), "junk: undecodable reply") {
		t.Errorf("undecodable: %v", err)
	}
	// Tool errors inside a result.
	cases := []struct{ tool, raw, want string }{
		{"click", `{"isError":true,"content":[{"type":"text","text":"off screen"}]}`, "click: off screen"},
		{"click", `{"isError":true,"content":[{"type":"text","text":"No screenshot of window 491"}],"structuredContent":{"code":"screenshot_context_missing","reason":"window_never_read"}}`, "click: window_never_read: No screenshot of window 491"},
		{"bring_to_front", `{"isError":true,"content":[{"type":"text","text":"foreground activation failed"}],"structuredContent":{"code":"foreground_unavailable","actual_foreground_window_id":7}}`, "bring_to_front: foreground activation failed; actual foreground window 7"},
	}
	for _, c := range cases {
		err := Check(c.tool, json.RawMessage(c.raw))
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v", c.raw, err)
		}
	}
	var de *Error
	if err := Check("bring_to_front", json.RawMessage(cases[2].raw)); !errors.As(err, &de) || de.ActualFront != 7 || de.Code != "foreground_unavailable" {
		t.Errorf("structured fields: %+v", de)
	}
	if err := Check("click", json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)); err != nil {
		t.Errorf("a plain result failed: %v", err)
	}
	// With ToolErrors, Call maps them itself.
	_, sock = newFake(t, func(int32, string, json.RawMessage) string {
		return `{"ok":true,"result":{"isError":true,"content":[{"type":"text","text":"off screen"}]}}`
	})
	c2 := &Client{Socket: sock, ToolErrors: true}
	defer c2.Close()
	if _, err := c2.Call(ctx, LaneInput, "click", nil); err == nil || err.Error() != "click: off screen" {
		t.Errorf("ToolErrors: %v", err)
	}
	// A transport refusal names the tool and the socket.
	c3 := &Client{Socket: filepath.Join(fakeDir(t), "none.sock"), ConnectWait: 300 * time.Millisecond}
	if _, err := c3.Call(ctx, LaneInput, "click", nil); err == nil || !strings.HasPrefix(err.Error(), "click: connect cua-driver at ") || !errors.As(err, &de) || !de.Transport || !de.Refused {
		t.Errorf("transport refusal: %v", err)
	}
}

// laterDriver listens at sock after delay and answers every line.
func laterDriver(t *testing.T, sock string, delay time.Duration) {
	t.Helper()
	ready := make(chan net.Listener, 1)
	go func() {
		time.Sleep(delay)
		_ = os.Remove(sock)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			ready <- nil
			return
		}
		ready <- ln
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					if _, err := r.ReadBytes('\n'); err != nil {
						return
					}
					_, _ = conn.Write([]byte(`{"ok":true,"result":{"width":1280}}` + "\n"))
				}
			}()
		}
	}()
	t.Cleanup(func() {
		if ln := <-ready; ln != nil {
			_ = ln.Close()
		}
	})
}

func TestWaitsOutDriverRestart(t *testing.T) {
	// The driver died and left its socket; the restarted one listens later.
	sock := filepath.Join(fakeDir(t), "d.sock")
	deadSocket(t, sock)
	laterDriver(t, sock, 600*time.Millisecond)
	c := &Client{Socket: sock}
	defer c.Close()
	start := time.Now()
	res, err := c.Call(context.Background(), LaneInput, "get_screen_size", nil)
	if err != nil || !strings.Contains(string(res), "1280") {
		t.Fatalf("%s %v", res, err)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Error("answered before the driver came back")
	}
	if ConnectRetry != 250*time.Millisecond || ConnectWait != 10*time.Second {
		t.Errorf("retry %s for %s", ConnectRetry, ConnectWait)
	}
}

func TestRequestWaitsForRestartingDriver(t *testing.T) {
	// No socket at all while the driver restarts.
	sock := filepath.Join(fakeDir(t), "d.sock")
	laterDriver(t, sock, 700*time.Millisecond)
	var dials atomic.Int32
	c := &Client{Socket: sock, Dial: func(ctx context.Context, s string) (net.Conn, error) {
		dials.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, "unix", s)
	}}
	defer c.Close()
	if _, err := c.Call(context.Background(), LaneCapture, "get_desktop_state", nil); err != nil {
		t.Fatal(err)
	}
	if n := dials.Load(); n < 3 || n > 5 {
		t.Errorf("%d dials over 700 ms, want one every 250 ms", n)
	}
	// One that never comes back fails at the bound.
	c2 := &Client{Socket: filepath.Join(fakeDir(t), "never.sock"), ConnectWait: 600 * time.Millisecond}
	start := time.Now()
	if _, err := c2.Call(context.Background(), LaneInput, "get_screen_size", nil); err == nil {
		t.Fatal("connected to nothing")
	}
	if took := time.Since(start); took < 400*time.Millisecond || took > 2*time.Second {
		t.Errorf("gave up after %s", took)
	}
}

func endedOnFirstConn(conn int32, name string, _ json.RawMessage) string {
	if conn == 1 {
		return ended
	}
	return `{"ok":true,"result":{"ok":true}}`
}

func TestSessionRestartOnEnded(t *testing.T) {
	f, sock := newFake(t, endedOnFirstConn)
	c := &Client{Socket: sock}
	defer c.Close()
	if _, err := c.Call(context.Background(), LaneInput, "press_key", map[string]any{"key": "a"}); err != nil {
		t.Fatal(err)
	}
	if n := f.conns.Load(); n != 2 {
		t.Errorf("%d sessions, want a new one after the end", n)
	}
	if n := f.sent("press_key"); n != 2 {
		t.Errorf("sent %d times, want once more after the restart", n)
	}
	// The new session goes on serving.
	if _, err := c.Call(context.Background(), LaneInput, "press_key", nil); err != nil || f.conns.Load() != 2 {
		t.Errorf("after restart: %v, %d sessions", err, f.conns.Load())
	}
}

func TestRestartsEndedSession(t *testing.T) {
	// An idle vision session the driver ended starts again under its lane.
	f, sock := newFake(t, endedOnFirstConn)
	c := &Client{Socket: sock}
	defer c.Close()
	if _, err := c.Call(context.Background(), "vision-agent-1", "get_desktop_state", nil); err != nil {
		t.Fatal(err)
	}
	if f.sent("get_desktop_state") != 2 || f.conns.Load() != 2 {
		t.Errorf("sent %d on %d sessions", f.sent("get_desktop_state"), f.conns.Load())
	}
}

func TestEndedSessionRestartsOnce(t *testing.T) {
	f, sock := newFake(t, func(int32, string, json.RawMessage) string { return ended })
	c := &Client{Socket: sock}
	defer c.Close()
	_, err := c.Call(context.Background(), "vision-a", "click", map[string]any{"x": 10.0, "y": 10.0})
	if err == nil || !strings.Contains(err.Error(), "session has ended") {
		t.Fatalf("err %v", err)
	}
	if n := f.sent("click"); n != 2 {
		t.Errorf("sent %d times, want exactly one resend", n)
	}
	// Another caller's session is unaffected by the first one ending.
	f2, sock2 := newFake(t, func(conn int32, _ string, _ json.RawMessage) string { return `{"ok":true,"result":1}` })
	c2 := &Client{Socket: sock2}
	defer c2.Close()
	_, _ = c2.Call(context.Background(), "vision-a", "get_screen_size", nil)
	_, _ = c2.Call(context.Background(), "vision-b", "get_screen_size", nil)
	c2.EndLane("vision-a")
	if _, err := c2.Call(context.Background(), "vision-b", "get_screen_size", nil); err != nil || f2.conns.Load() != 2 {
		t.Errorf("vision-b after vision-a ended: %v (%d sessions)", err, f2.conns.Load())
	}
	if _, err := c2.Call(context.Background(), "vision-a", "get_screen_size", nil); err != nil || f2.conns.Load() != 3 {
		t.Errorf("vision-a starts again: %v (%d sessions)", err, f2.conns.Load())
	}
}

func lookalikes(_ int32, name string, _ json.RawMessage) string {
	switch name {
	case "mention":
		return `{"ok":false,"error":"window 4: its session has ended earlier"}`
	case "result":
		return `{"ok":true,"result":{"isError":true,"content":[{"type":"text","text":"session has ended"}]}}`
	}
	return `{"ok":false,"error":"press_key failed: the session has ended for this app"}`
}

func TestNoRetryOnLookalikeError(t *testing.T) {
	f, sock := newFake(t, lookalikes)
	c := &Client{Socket: sock}
	defer c.Close()
	for _, tool := range []string{"mention", "other", "result"} {
		_, _ = c.Call(context.Background(), LaneInput, tool, nil)
	}
	if f.total() != 3 || f.conns.Load() != 1 {
		t.Errorf("%d requests on %d sessions, want each once on one", f.total(), f.conns.Load())
	}
}

func TestResendOnlyOnSessionEnded(t *testing.T) {
	f, sock := newFake(t, func(conn int32, name string, args json.RawMessage) string {
		if name == "type_text" {
			if conn == 1 {
				return ended
			}
			return `{"ok":true,"result":1}`
		}
		return lookalikes(conn, name, args)
	})
	c := &Client{Socket: sock}
	defer c.Close()
	if _, err := c.Call(context.Background(), LaneInput, "type_text", map[string]any{"text": "hi"}); err != nil {
		t.Fatal(err)
	}
	if f.sent("type_text") != 2 {
		t.Errorf("type_text sent %d times", f.sent("type_text"))
	}
	for _, tool := range []string{"mention", "other"} {
		if _, err := c.Call(context.Background(), LaneInput, tool, nil); err == nil {
			t.Errorf("%s succeeded", tool)
		}
		if f.sent(tool) != 1 {
			t.Errorf("%s resent", tool)
		}
	}
}

func TestCaptureSessionSeparate(t *testing.T) {
	var mu sync.Mutex
	byConn := map[int32][]string{}
	captures := 0
	f, sock := newFake(t, func(conn int32, name string, _ json.RawMessage) string {
		mu.Lock()
		byConn[conn] = append(byConn[conn], name)
		if name == "get_desktop_state" {
			captures++
		}
		n := captures
		mu.Unlock()
		if name == "get_desktop_state" && n == 1 {
			return ended
		}
		return `{"ok":true,"result":1}`
	})
	c := &Client{Socket: sock}
	defer c.Close()
	ctx := context.Background()
	if _, err := c.Call(ctx, LaneInput, "press_key", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(ctx, LaneCapture, "get_desktop_state", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(ctx, LaneInput, "press_key", nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	// conn 1 is input, 2 the first capture session, 3 the restarted one;
	// the input session never saw a capture and was never restarted.
	if f.conns.Load() != 3 || strings.Join(byConn[1], ",") != "press_key,press_key" {
		t.Errorf("sessions %v", byConn)
	}
}

func TestReadRetriedOnceAfterEmptyReply(t *testing.T) {
	var calls atomic.Int32
	var times []time.Time
	var mu sync.Mutex
	f, sock := newFake(t, func(_ int32, name string, _ json.RawMessage) string {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		if calls.Add(1) == 1 {
			return "" // hang up without a reply
		}
		return `{"ok":true,"result":{"windows":[]}}`
	})
	c := &Client{Socket: sock}
	defer c.Close()
	if _, err := c.Call(context.Background(), LaneInput, "list_windows", nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gap := times[1].Sub(times[0])
	mu.Unlock()
	if f.sent("list_windows") != 2 || gap < ReadRetry-20*time.Millisecond {
		t.Errorf("sent %d times, %s apart", f.sent("list_windows"), gap)
	}
	// An empty result counts too, and the retry happens only once.
	f2, sock2 := newFake(t, func(int32, string, json.RawMessage) string { return `{"ok":true}` })
	c2 := &Client{Socket: sock2}
	defer c2.Close()
	for _, tool := range []string{"get_window_state", "get_desktop_state", "end_session"} {
		if _, err := c2.Call(context.Background(), LaneCapture, tool, nil); err == nil {
			t.Errorf("%s: empty twice succeeded", tool)
		}
		if n := f2.sent(tool); n != 2 {
			t.Errorf("%s sent %d times", tool, n)
		}
	}
}

func TestInputNeverRetried(t *testing.T) {
	f, sock := newFake(t, func(_ int32, name string, _ json.RawMessage) string {
		if name == "drag" {
			return `{"ok":true}`
		}
		return ""
	})
	c := &Client{Socket: sock}
	defer c.Close()
	for _, tool := range []string{"click", "type_text", "press_key", "hotkey", "scroll", "drag"} {
		_, err := c.Call(context.Background(), LaneInput, tool, map[string]any{"x": 1.0, "y": 1.0, "count": 2})
		if err == nil {
			t.Errorf("%s succeeded", tool)
		}
		if tool != "drag" && !strings.Contains(err.Error(), "may have been sent") {
			t.Errorf("%s: %v", tool, err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if f.total() != 6 {
		t.Errorf("%d requests for 6 inputs", f.total())
	}
}

// clickLog is a fake driver that records when each click arrived and ended.
type clickLog struct {
	mu    sync.Mutex
	start []time.Time
	end   []time.Time
	hold  time.Duration
}

func (l *clickLog) answer(_ int32, name string, _ json.RawMessage) string {
	if name != "click" {
		return `{"ok":true,"result":1}`
	}
	l.mu.Lock()
	l.start = append(l.start, time.Now())
	hold := l.hold
	l.mu.Unlock()
	time.Sleep(hold)
	l.mu.Lock()
	l.end = append(l.end, time.Now())
	l.mu.Unlock()
	return `{"ok":true,"result":1}`
}

func click(x, y float64) map[string]any { return map[string]any{"x": x, "y": y} }

func TestClickSpacing(t *testing.T) {
	l := &clickLog{}
	_, sock := newFake(t, l.answer)
	c := &Client{Socket: sock}
	defer c.Close()
	ctx := context.Background()
	start := time.Now()
	_, _ = c.Call(ctx, LaneInput, "click", click(100, 100))
	_, _ = c.Call(ctx, LaneInput, "click", click(110, 108)) // 12.8 points away
	if gap := l.start[1].Sub(l.end[0]); gap < ClickSpacing-30*time.Millisecond {
		t.Errorf("near clicks %s apart", gap)
	}
	far := time.Now()
	_, _ = c.Call(ctx, LaneInput, "click", click(300, 100))
	if d := time.Since(far); d > 200*time.Millisecond {
		t.Errorf("a far click waited %s", d)
	}
	// A double click and a click in another window are not spaced.
	t2 := time.Now()
	_, _ = c.Call(ctx, LaneInput, "click", map[string]any{"x": 300.0, "y": 100.0, "count": 2})
	_, _ = c.Call(ctx, LaneInput, "click", map[string]any{"x": 300.0, "y": 100.0, "pid": 5, "window_id": 9})
	if d := time.Since(t2); d > 300*time.Millisecond {
		t.Errorf("unrelated clicks waited %s", d)
	}
	_ = start
}

func TestNearbyClickWaitsOutDoubleClickTime(t *testing.T) {
	l := &clickLog{}
	_, sock := newFake(t, l.answer)
	c := &Client{Socket: sock}
	defer c.Close()
	ctx := context.Background()
	_, _ = c.Call(ctx, LaneInput, "click", click(50, 50))
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	_, _ = c.Call(ctx, LaneInput, "click", click(50, 66)) // exactly 16 away
	waited := time.Since(start)
	if waited < 250*time.Millisecond || waited > 450*time.Millisecond {
		t.Errorf("waited %s, want the rest of %s after 200 ms", waited, ClickSpacing)
	}
	// 17 points away goes at once.
	time.Sleep(10 * time.Millisecond)
	start = time.Now()
	_, _ = c.Call(ctx, LaneInput, "click", click(50, 83))
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Errorf("a click 17 points away waited %s", d)
	}
}

func TestClickSpacingAfterCancel(t *testing.T) {
	l := &clickLog{hold: 300 * time.Millisecond}
	_, sock := newFake(t, l.answer)
	c := &Client{Socket: sock}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, LaneInput, "click", click(20, 20)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	cancelled := time.Now()
	l.mu.Lock()
	l.hold = 0
	l.mu.Unlock()
	_, _ = c.Call(context.Background(), LaneInput, "click", click(22, 22))
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.start) != 2 {
		t.Fatalf("%d clicks", len(l.start))
	}
	if gap := l.start[1].Sub(cancelled); gap < ClickSpacing-30*time.Millisecond {
		t.Errorf("click %s after the cancelled one", gap)
	}
}

func TestConcurrentNearClicks(t *testing.T) {
	l := &clickLog{hold: 100 * time.Millisecond}
	_, sock := newFake(t, l.answer)
	c := &Client{Socket: sock}
	defer c.Close()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Separate lanes, so only the spacing orders them.
			_, _ = c.Call(context.Background(), []string{LaneInput, "vision-x"}[i], "click", click(40, 40))
		}()
	}
	wg.Wait()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.start) != 2 {
		t.Fatalf("%d clicks", len(l.start))
	}
	if l.start[1].Before(l.end[0]) {
		t.Fatal("the second click started before the first ended")
	}
	if gap := l.start[1].Sub(l.end[0]); gap < ClickSpacing-30*time.Millisecond {
		t.Errorf("concurrent near clicks %s apart", gap)
	}
}

// fakeActivity is a user-activity check that counts own input.
type fakeActivity struct {
	busy bool
	own  atomic.Int32
}

func (a *fakeActivity) Check() *protocol.Error {
	if a.busy {
		return &protocol.Error{Code: protocol.ErrBusy, Message: "the user used the mouse or keyboard 0.2 s ago, so fennec-cua yields; nothing was sent", Hint: "retrying after the user stops is safe"}
	}
	return nil
}

func (a *fakeActivity) Own() { a.own.Add(1) }

func TestOnlyForegroundInputIsOwn(t *testing.T) {
	_, sock := newFake(t, func(int32, string, json.RawMessage) string { return `{"ok":true,"result":1}` })
	act := &fakeActivity{}
	c := &Client{Socket: sock, Activity: act}
	defer c.Close()
	ctx := context.Background()
	steps := []struct {
		tool string
		args map[string]any
		own  int32
	}{
		{"click", click(1, 1), 1},
		{"type_text", map[string]any{"text": "a", "delivery_mode": "background"}, 1},
		{"press_key", map[string]any{"key": "a", "delivery_mode": "foreground"}, 2},
		{"bring_to_front", map[string]any{"pid": 1}, 2},
		{"list_windows", nil, 2},
		{"scroll", map[string]any{"delivery_mode": "background"}, 2},
	}
	for _, s := range steps {
		if _, err := c.Call(ctx, LaneInput, s.tool, s.args); err != nil {
			t.Fatalf("%s: %v", s.tool, err)
		}
		if n := act.own.Load(); n != s.own {
			t.Errorf("after %s own input counted %d times, want %d", s.tool, n, s.own)
		}
	}
}

func TestBringToFrontRefusedWhileBusy(t *testing.T) {
	f, sock := newFake(t, func(int32, string, json.RawMessage) string { return `{"ok":true,"result":1}` })
	act := &fakeActivity{busy: true}
	c := &Client{Socket: sock, Activity: act}
	defer c.Close()
	ctx := context.Background()
	for _, tool := range []string{"bring_to_front", "click", "type_text"} {
		_, err := c.Call(ctx, LaneInput, tool, click(1, 1))
		var de *Error
		if !errors.As(err, &de) || !de.Refused || de.Code != string(protocol.ErrBusy) || !strings.Contains(err.Error(), "nothing was sent") || !strings.Contains(err.Error(), "retrying after the user stops is safe") {
			t.Errorf("%s: %v", tool, err)
		}
	}
	if f.total() != 0 {
		t.Errorf("%d requests reached the driver", f.total())
	}
	if act.own.Load() != 0 {
		t.Error("a refusal counted as own input")
	}
	// Background input and reads are not checked.
	if _, err := c.Call(ctx, LaneInput, "type_text", map[string]any{"text": "a", "delivery_mode": "background"}); err != nil {
		t.Errorf("background typing: %v", err)
	}
	if _, err := c.Call(ctx, LaneInput, "list_windows", nil); err != nil {
		t.Errorf("read: %v", err)
	}
}

func TestLockedScreenRefusal(t *testing.T) {
	_, sock := newFake(t, func(_ int32, name string, _ json.RawMessage) string {
		if name == "press_key" {
			return `{"ok":false,"error":"window 7 cannot receive input: the screen is locked"}`
		}
		return `{"ok":true,"result":{"isError":true,"content":[{"type":"text","text":"window 7 has no accessibility tree"}],"structuredContent":{"code":"screen_locked","status":"refused"}}}`
	})
	c := &Client{Socket: sock}
	defer c.Close()
	_, err := c.Call(context.Background(), LaneInput, "press_key", map[string]any{"key": "a"})
	var de *Error
	if !errors.As(err, &de) || !de.Locked() || !strings.Contains(err.Error(), "the screen is locked") || !strings.Contains(err.Error(), "unlock the Mac") {
		t.Errorf("press_key: %v", err)
	}
	raw, err := c.Call(context.Background(), LaneInput, "click", click(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	err = Check("click", raw)
	if !errors.As(err, &de) || !de.Locked() || !de.Refused || !strings.Contains(err.Error(), "unlock the Mac") {
		t.Errorf("click: %v", err)
	}
}

func TestDisplayAsleep(t *testing.T) {
	_, sock := newFake(t, func(int32, string, json.RawMessage) string {
		return `{"ok":true,"result":{"isError":true,"content":[{"type":"text","text":"Desktop screenshot failed: the display is off"}],"structuredContent":{"status":"refused","code":"display_asleep","refusal":{"code":"display_asleep","message":"Desktop screenshot failed: the display is off"}}}}`
	})
	c := &Client{Socket: sock, ToolErrors: true}
	defer c.Close()
	_, err := c.Call(context.Background(), LaneCapture, "get_desktop_state", nil)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeDisplayAsleep || !de.Refused || !strings.HasPrefix(err.Error(), "get_desktop_state: display_asleep: ") {
		t.Errorf("err %v", err)
	}
}

func TestWindowStateReadReuse(t *testing.T) {
	var forget atomic.Bool
	f, sock := newFake(t, func(_ int32, name string, _ json.RawMessage) string {
		if name == "click" && forget.Swap(false) {
			return `{"ok":true,"result":{"isError":true,"content":[{"type":"text","text":"No screenshot of window 9"}],"structuredContent":{"code":"screenshot_context_missing","reason":"window_never_read"}}}`
		}
		return `{"ok":true,"result":{"content":[]}}`
	})
	c := &Client{Socket: sock}
	defer c.Close()
	ctx := context.Background()
	at := [4]float64{10, 20, 300, 200}
	do := func(bounds [4]float64, scale float64) {
		t.Helper()
		if _, err := c.WindowCall(ctx, LaneInput, 5, 9, bounds, scale, "click", map[string]any{"x": 3.0, "y": 4.0, "window_id": 9}); err != nil {
			t.Fatal(err)
		}
	}
	reads := func() int { return f.sent("get_window_state") }
	do(at, 2)
	do(at, 2)
	if reads() != 1 {
		t.Errorf("unmoved window read %d times", reads())
	}
	do([4]float64{11, 20, 300, 200}, 2)
	if reads() != 2 {
		t.Errorf("moved window: %d reads", reads())
	}
	do([4]float64{11, 20, 300, 200}, 1.5)
	if reads() != 3 {
		t.Errorf("scale change: %d reads", reads())
	}
	forget.Store(true)
	do([4]float64{11, 20, 300, 200}, 1.5)
	if reads() != 4 || f.sent("click") != 6 {
		t.Errorf("forgotten window: %d reads, %d clicks", reads(), f.sent("click"))
	}
	// At scale 1 window pixels are points: no read.
	c2 := &Client{Socket: sock}
	defer c2.Close()
	before := reads()
	if _, err := c2.WindowCall(ctx, LaneInput, 5, 10, at, 1, "click", map[string]any{"x": 300.0, "y": 300.0, "window_id": 10}); err != nil {
		t.Fatal(err)
	}
	if reads() != before {
		t.Error("read a window at scale 1")
	}
}
