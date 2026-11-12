package mcpserver

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/sched"
)

// scriptedHandler is a Handler whose tools are functions.
type scriptedHandler struct {
	tools []string
	mu    sync.Mutex
	calls []string
	fn    func(ctx context.Context, s *Session, name string, args json.RawMessage) Result
}

func (h *scriptedHandler) Tools(*Session) []Tool {
	out := make([]Tool, 0, len(h.tools))
	for _, n := range h.tools {
		out = append(out, Tool{Name: n, InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	return out
}

func (h *scriptedHandler) Call(ctx context.Context, s *Session, name string, args json.RawMessage) Result {
	h.mu.Lock()
	h.calls = append(h.calls, name)
	h.mu.Unlock()
	return h.fn(ctx, s, name, args)
}

func (h *scriptedHandler) called() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

func serveInfo(t *testing.T, h Handler, info Info) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		_ = Serve(context.Background(), inR, outW, h, info)
		_ = outW.Close()
	}()
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	t.Cleanup(func() { _ = inW.Close() })
	return &harness{t: t, in: inW, out: sc}
}

// recvWithin reads one reply or fails after d.
func (h *harness) recvWithin(d time.Duration) map[string]any {
	h.t.Helper()
	got := make(chan map[string]any, 1)
	go func() {
		if !h.out.Scan() {
			got <- nil
			return
		}
		var m map[string]any
		_ = json.Unmarshal(h.out.Bytes(), &m)
		got <- m
	}()
	select {
	case m := <-got:
		if m == nil {
			h.t.Fatal("stream ended")
		}
		return m
	case <-time.After(d):
		h.t.Fatal("no reply in time")
	}
	return nil
}

func errCode(m map[string]any) float64 {
	e, _ := m["error"].(map[string]any)
	if e == nil {
		return 0
	}
	c, _ := e["code"].(float64)
	return c
}

func errMessage(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	s, _ := e["message"].(string)
	return s
}

func TestInitialize(t *testing.T) {
	h := &scriptedHandler{tools: []string{"observe", "act", "wait", "run", "flow", "status", "plugin_x"}, fn: func(context.Context, *Session, string, json.RawMessage) Result {
		return Result{Text: `{"version":"1.2.3"}`}
	}}
	c := serveInfo(t, h, Info{Name: "fennec-cua", Version: "1.2.3", Instructions: strings.Repeat("i", 5000)})
	c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"x"}}}`)
	r := c.recv()["result"].(map[string]any)
	if r["protocolVersion"] != "2025-06-18" || r["serverInfo"].(map[string]any)["name"] != "fennec-cua" {
		t.Errorf("initialize %v", r)
	}
	if n := len(r["instructions"].(string)); n > 2048 {
		t.Errorf("instructions %d chars", n)
	}
	// Notifications are never answered.
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	c.send(`{"jsonrpc":"2.0","method":"notifications/whatever","params":{}}`)
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	m := c.recv()
	if m["id"] != 2.0 {
		t.Fatalf("a notification was answered: %v", m)
	}
	var names []string
	for _, tl := range m["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if strings.Join(names[:6], ",") != "observe,act,wait,run,flow,status" {
		t.Errorf("tools %v", names)
	}
	c.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status"}}`)
	if text := c.recv()["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]; !strings.Contains(text.(string), "version") {
		t.Errorf("status %v", text)
	}
}

func TestProtocolVersion(t *testing.T) {
	if strings.Join(Versions, ",") != "2025-11-25,2025-06-18,2025-03-26" {
		t.Fatalf("versions %v", Versions)
	}
	c := serveInfo(t, &scriptedHandler{}, Info{Name: "t"})
	for i, v := range append(append([]string{}, Versions...), "2024-11-05", "", "garbage") {
		params := `{}`
		if v != "" {
			params = fmt.Sprintf(`{"protocolVersion":%q}`, v)
		}
		c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"initialize","params":%s}`, i+1, params))
		got := c.recv()["result"].(map[string]any)["protocolVersion"]
		want := v
		if i >= len(Versions) {
			want = Versions[0]
		}
		if got != want {
			t.Errorf("client %q: answered %v, want %s", v, got, want)
		}
	}
}

func TestResultContent(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nrest"))
	h := &scriptedHandler{tools: []string{"shot", "plain", "fails"}, fn: func(_ context.Context, _ *Session, name string, _ json.RawMessage) Result {
		switch name {
		case "shot":
			return Result{Text: "captured", Images: []Image{{Data: png, MIMEType: "image/png"}}, Structured: map[string]any{"ok": true}}
		case "plain":
			return Result{Text: "hello"}
		}
		return Result{Text: "boom: the window went away", IsError: true}
	}}
	c := serveInfo(t, h, Info{})
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"shot","arguments":{}}}`)
	r := c.recv()["result"].(map[string]any)
	blocks := r["content"].([]any)
	if len(blocks) != 2 || blocks[0].(map[string]any)["text"] != "captured" {
		t.Fatalf("blocks %v", blocks)
	}
	if img := blocks[1].(map[string]any); img["type"] != "image" || img["data"] != png || img["mimeType"] != "image/png" {
		t.Errorf("image %v", img)
	}
	if r["isError"] != false || r["structuredContent"].(map[string]any)["ok"] != true {
		t.Errorf("result %v", r)
	}
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"plain"}}`)
	if blocks := c.recv()["result"].(map[string]any)["content"].([]any); len(blocks) != 1 {
		t.Errorf("a result without a capture has %d blocks", len(blocks))
	}
	c.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fails"}}`)
	m := c.recv()
	if m["error"] != nil {
		t.Fatalf("a failed call became a protocol error: %v", m)
	}
	r = m["result"].(map[string]any)
	if r["isError"] != true || !strings.Contains(r["content"].([]any)[0].(map[string]any)["text"].(string), "boom") {
		t.Errorf("failed call %v", r)
	}
}

func TestJSONRPCErrors(t *testing.T) {
	c := serveInfo(t, &scriptedHandler{tools: []string{"echo"}}, Info{})
	c.send(`{"jsonrpc":"2.0","id":1,"method":`)
	m := c.recv()
	if errCode(m) != CodeParse || m["id"] != nil {
		t.Errorf("parse error %v", m)
	}
	if _, has := m["id"]; !has {
		t.Error("parse error without an id member")
	}
	c.send(`{"jsonrpc":"2.0","id":2,"method":"no/such"}`)
	if m := c.recv(); errCode(m) != CodeMethodNotFound || m["id"] != 2.0 {
		t.Errorf("unknown method %v", m)
	}
	c.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nosuch"}}`)
	if m := c.recv(); errCode(m) != CodeInvalidParams || !strings.Contains(errMessage(m), "nosuch") {
		t.Errorf("unknown tool %v", m)
	}
	c.send(`{"jsonrpc":"2.0","id":4,"params":{}}`)
	if m := c.recv(); errCode(m) != CodeInvalidRequest || m["id"] != 4.0 {
		t.Errorf("no method %v", m)
	}
}

var stdioID = regexp.MustCompile(`^mcp-([A-Za-z0-9-]{1,32})-[0-9a-f]{12}$`)

func agentOf(t *testing.T, c *harness, id int) string {
	t.Helper()
	c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"who"}}`, id))
	return c.recv()["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
}

func whoHandler() *scriptedHandler {
	return &scriptedHandler{tools: []string{"who"}, fn: func(_ context.Context, s *Session, _ string, _ json.RawMessage) Result {
		return Result{Text: s.AgentID}
	}}
}

func TestAgentPerSession(t *testing.T) {
	h := whoHandler()
	a, b := serveInfo(t, h, Info{IDPrefix: "mcp"}), serveInfo(t, h, Info{IDPrefix: "mcp"})
	for _, c := range []*harness{a, b} {
		c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"claude-code"}}}`)
		c.recv()
	}
	a1, a2, b1 := agentOf(t, a, 2), agentOf(t, a, 3), agentOf(t, b, 2)
	if a1 != a2 {
		t.Errorf("one session, two ids: %s %s", a1, a2)
	}
	if a1 == b1 {
		t.Errorf("two sessions of the same client share %s", a1)
	}
	if !stdioID.MatchString(a1) || !strings.HasPrefix(a1, "mcp-claude-code-") {
		t.Errorf("id %q", a1)
	}
}

func TestAgentID(t *testing.T) {
	if got := agentID("mcp", "Claude Code/2.0 é", ""); !stdioID.MatchString(got) || !strings.HasPrefix(got, "mcp-Claude-Code-2-0---") {
		t.Errorf("stdio id %q", got)
	}
	if got := agentID("mcp", "", ""); !strings.HasPrefix(got, "mcp-client-") {
		t.Errorf("nameless client %q", got)
	}
	long := strings.Repeat("a", 50)
	if got := agentID("mcp", long, ""); !strings.HasPrefix(got, "mcp-"+strings.Repeat("a", 32)+"-") || len(got) != 4+32+1+12 {
		t.Errorf("long client %q", got)
	}
	if got := agentID("mcp-http", "ignored", "ab:cd/"+long); got != "mcp-http-ab-cd-"+strings.Repeat("a", 26) {
		t.Errorf("http id %q", got)
	}
}

// turnHandler runs "hold" with the desk turn until cancelled and "take",
// which takes the turn and answers at once.
func turnHandler(turn *sched.Turn, started chan string, ended chan string) *scriptedHandler {
	return &scriptedHandler{tools: []string{"hold", "take", "ping"}, fn: func(ctx context.Context, s *Session, name string, args json.RawMessage) Result {
		var a struct {
			Tag string `json:"tag"`
		}
		_ = json.Unmarshal(args, &a)
		switch name {
		case "hold":
			release, err := turn.Acquire(ctx, a.Tag)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			defer release()
			started <- a.Tag
			<-ctx.Done()
			ended <- a.Tag
			return Result{Text: "ended"}
		case "take":
			started <- "queued:" + a.Tag
			release, err := turn.Acquire(ctx, a.Tag)
			if err != nil {
				ended <- a.Tag
				return Result{Text: err.Error(), IsError: true}
			}
			release()
			return Result{Text: "took " + a.Tag}
		}
		return Result{Text: "pong"}
	}}
}

func TestCancelRunning(t *testing.T) {
	turn := &sched.Turn{Busy: 10 * time.Second}
	started, ended := make(chan string, 8), make(chan string, 8)
	c := serveInfo(t, turnHandler(turn, started, ended), Info{})
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hold","arguments":{"tag":"a"}}}`)
	<-started
	at := time.Now()
	c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the running call was not interrupted within 3 s")
	}
	// The turn is free: the next run starts at once, and the cancelled
	// request never answers.
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"take","arguments":{"tag":"b"}}}`)
	m := c.recvWithin(5 * time.Second)
	if m["id"] != 2.0 {
		t.Fatalf("got %v, want the next call's reply only", m)
	}
	if time.Since(at) > 5*time.Second {
		t.Error("the next run waited for the cancelled one")
	}
	c.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ping"}}`)
	if m := c.recv(); m["id"] != 3.0 {
		t.Errorf("later request %v", m)
	}
}

func TestCancelQueued(t *testing.T) {
	turn := &sched.Turn{Busy: 10 * time.Second}
	started, ended := make(chan string, 8), make(chan string, 8)
	c := serveInfo(t, turnHandler(turn, started, ended), Info{})
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hold","arguments":{"tag":"a"}}}`)
	<-started
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"take","arguments":{"tag":"b"}}}`)
	if s := <-started; s != "queued:b" {
		t.Fatalf("started %s", s)
	}
	c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2}}`)
	select {
	case tag := <-ended:
		if tag != "b" {
			t.Fatalf("ended %s", tag)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the queued call kept waiting")
	}
	if turn.Holder() != "a" {
		t.Errorf("holder %q: the running call was disturbed", turn.Holder())
	}
	// End the running one; replies keep request order and skip the
	// cancelled request.
	c.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ping"}}`)
	c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	if m := c.recvWithin(3 * time.Second); m["id"] != 3.0 {
		t.Errorf("got %v, want only the ping", m)
	}
}

func TestDisconnectCancelsCall(t *testing.T) {
	started := make(chan struct{}, 2)
	cancelled := make(chan struct{}, 2)
	h := &scriptedHandler{tools: []string{"block"}, fn: func(ctx context.Context, _ *Session, _ string, _ json.RawMessage) Result {
		started <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
		return Result{}
	}}
	inR, inW := io.Pipe()
	go func() { _ = Serve(context.Background(), inR, io.Discard, h, Info{}) }()
	_, _ = inW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"block"}}` + "\n"))
	<-started
	select {
	case <-cancelled:
		t.Fatal("an open connection cancelled its call")
	case <-time.After(300 * time.Millisecond):
	}
	_ = inW.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("the call outlived its connection")
	}
}

func TestUnknownToolError(t *testing.T) {
	h := &scriptedHandler{tools: []string{"observe"}}
	c := serveInfo(t, h, Info{})
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"obsrve","arguments":{}}}`)
	m := c.recv()
	if errCode(m) != CodeInvalidParams || !strings.Contains(errMessage(m), "obsrve") {
		t.Errorf("unknown tool %v", m)
	}
	if len(h.called()) != 0 {
		t.Error("an unknown tool reached the handler")
	}
}

func nonObject(t *testing.T, tools ...string) (*harness, *scriptedHandler) {
	h := &scriptedHandler{tools: tools, fn: func(context.Context, *Session, string, json.RawMessage) Result { return Result{Text: "ran"} }}
	return serveInfo(t, h, Info{}), h
}

func TestArgsNotObject(t *testing.T) {
	c, h := nonObject(t, "act")
	for i, args := range []string{`[1,2]`, `"steps"`, `7`, `true`} {
		c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"act","arguments":%s}}`, i+1, args))
		m := c.recv()
		if errCode(m) != CodeInvalidParams || !strings.Contains(errMessage(m), "act") || !strings.Contains(errMessage(m), "object") {
			t.Errorf("%s: %v", args, m)
		}
	}
	if len(h.called()) != 0 {
		t.Errorf("handler called for bad arguments: %v", h.called())
	}
}

func TestNonObjectArgs(t *testing.T) {
	c, h := nonObject(t, "observe")
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe","arguments":"scope=desktop"}}`)
	if m := c.recv(); errCode(m) != CodeInvalidParams || !strings.Contains(errMessage(m), "observe") {
		t.Errorf("non-object %v", m)
	}
	// The connection stays usable; null and missing arguments are an empty
	// object.
	for i, args := range []string{`{}`, `null`} {
		c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"observe","arguments":%s}}`, i+2, args))
		if m := c.recv(); m["error"] != nil {
			t.Errorf("%s: %v", args, m)
		}
	}
	c.send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"observe"}}`)
	if m := c.recv(); m["error"] != nil {
		t.Errorf("missing arguments: %v", m)
	}
	if got := len(h.called()); got != 3 {
		t.Errorf("handler called %d times", got)
	}
}

func TestNonObjectArgsRejected(t *testing.T) {
	c, h := nonObject(t, "observe", "act")
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe","arguments":["vision"]}}`)
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"act","arguments":[{"click":[1,2],"view":"v1"}]}}`)
	for _, tool := range []string{"observe", "act"} {
		m := c.recv()
		if errCode(m) != CodeInvalidParams || !strings.Contains(errMessage(m), "invalid arguments for "+tool) {
			t.Errorf("%s: %v", tool, m)
		}
	}
	if len(h.called()) != 0 {
		t.Error("a vision call with bad arguments reached the tool (and so the driver)")
	}
}

func TestOversizedMessageRefused(t *testing.T) {
	if MaxMessage != 64<<20 {
		t.Fatalf("MaxMessage %d", MaxMessage)
	}
	h := &scriptedHandler{tools: []string{"echo"}, fn: func(context.Context, *Session, string, json.RawMessage) Result { return Result{Text: "ok"} }}
	// The real limit: 64 MiB and one byte more is refused, then the
	// connection goes on.
	c := serveInfo(t, h, Info{})
	go func() {
		chunk := make([]byte, 1<<20)
		for i := range chunk {
			chunk[i] = 'x'
		}
		_, _ = c.in.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"blob":"`))
		for i := 0; i < 64; i++ {
			_, _ = c.in.Write(chunk)
		}
		_, _ = c.in.Write([]byte(`"}}}` + "\n"))
		_, _ = c.in.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"))
	}()
	m := c.recvWithin(20 * time.Second)
	if errCode(m) != CodeInvalidRequest || !strings.Contains(errMessage(m), "64 MiB") {
		t.Fatalf("oversized: %v", m)
	}
	if m := c.recvWithin(5 * time.Second); m["id"] != 2.0 {
		t.Errorf("after the refusal: %v", m)
	}
	if len(h.called()) != 0 {
		t.Error("the oversized call ran")
	}
	// Exactly at a (small) limit passes; one byte over does not.
	small := serveInfo(t, h, Info{MaxMessage: 64})
	line := `{"jsonrpc":"2.0","id":3,"method":"ping"}`
	line += strings.Repeat(" ", 64-len(line))
	small.send(line)
	if m := small.recv(); m["id"] != 3.0 {
		t.Errorf("at the limit: %v", m)
	}
	small.send(line + " ")
	if m := small.recv(); errCode(m) != CodeInvalidRequest {
		t.Errorf("over the limit: %v", m)
	}
}

func TestImageMimeType(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString
	jpeg := enc([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F'})
	webp := enc([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "))
	png := enc([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR"))
	h := &scriptedHandler{tools: []string{"ext_shot"}, fn: func(context.Context, *Session, string, json.RawMessage) Result {
		// An extension tool labels everything PNG, or nothing.
		return Result{Images: []Image{{Data: jpeg, MIMEType: "image/png"}, {Data: webp}, {Data: png, MIMEType: "image/jpeg"}, {Data: enc([]byte("plain")), MIMEType: "image/gif"}}}
	}}
	c := serveInfo(t, h, Info{})
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ext_shot"}}`)
	blocks := c.recv()["result"].(map[string]any)["content"].([]any)
	want := []string{"image/jpeg", "image/webp", "image/png", "image/gif"}
	if len(blocks) != len(want) {
		t.Fatalf("blocks %v", blocks)
	}
	for i, b := range blocks {
		if got := b.(map[string]any)["mimeType"]; got != want[i] {
			t.Errorf("image %d labelled %v, want %s", i, got, want[i])
		}
	}
}
