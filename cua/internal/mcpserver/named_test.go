package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// closingHandler is fakeHandler that records closed sessions.
type closingHandler struct {
	fakeHandler
	mu     sync.Mutex
	closed []*Session
}

func (h *closingHandler) CloseSession(s *Session) {
	h.mu.Lock()
	h.closed = append(h.closed, s)
	h.mu.Unlock()
}

func (h *closingHandler) closedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.closed)
}

// connect runs one Serve connection, sends lines and returns the replies
// once the connection has ended.
func connect(t *testing.T, h Handler, info Info, lines ...string) []map[string]any {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan struct{})
	go func() {
		_ = Serve(context.Background(), inR, outW, h, info)
		_ = outW.Close()
		close(done)
	}()
	var replies []map[string]any
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	// Each line is a request: wait for its reply before the next, so the
	// stream ends only after every call has answered.
	for _, l := range lines {
		if _, err := inW.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
		if !sc.Scan() {
			t.Fatalf("no reply to %s", l)
		}
		var m map[string]any
		_ = json.Unmarshal(sc.Bytes(), &m)
		replies = append(replies, m)
	}
	_ = inW.Close()
	for sc.Scan() {
	}
	<-done
	return replies
}

func initWith(session string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"cli"},"_meta":{"fennec-cua/session":"` + session + `"}}}`
}

const echoCall = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`

func echoAgent(t *testing.T, replies []map[string]any) string {
	t.Helper()
	for _, r := range replies {
		if r["id"] != 2.0 {
			continue
		}
		res, _ := r["result"].(map[string]any)
		content, _ := res["content"].([]any)
		if len(content) == 0 {
			t.Fatalf("no content: %v", r)
		}
		text := content[0].(map[string]any)["text"].(string)
		_, agent, _ := strings.Cut(text, "@")
		return agent
	}
	t.Fatalf("no echo reply in %v", replies)
	return ""
}

func TestNamedSessionSurvivesConnections(t *testing.T) {
	h := &closingHandler{fakeHandler: fakeHandler{started: make(chan string, 4)}}
	named := &Named{Idle: time.Hour}
	info := Info{Name: "t", Version: "0", IDPrefix: "mcp", Named: named}

	a := echoAgent(t, connect(t, h, info, initWith("work"), echoCall))
	b := echoAgent(t, connect(t, h, info, initWith("work"), echoCall))
	if a != b || a != "mcp-session-work" {
		t.Fatalf("named connections got agents %q and %q", a, b)
	}
	if n := h.closedCount(); n != 0 {
		t.Fatalf("a named session was closed with its connection (%d closes)", n)
	}
	other := echoAgent(t, connect(t, h, info, initWith("other"), echoCall))
	if other == a {
		t.Fatal("two names share one session")
	}
	// An unnamed connection is closed with its connection, as before.
	plain := echoAgent(t, connect(t, h, info, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, echoCall))
	if strings.HasPrefix(plain, "mcp-session-") || h.closedCount() != 1 {
		t.Fatalf("unnamed connection: agent %q, %d closes", plain, h.closedCount())
	}
	// Ending the session closes it when the connection ends.
	r := connect(t, h, info, initWith("work"), `{"jsonrpc":"2.0","id":3,"method":"`+EndSessionMethod+`"}`)
	if len(r) != 2 || r[1]["error"] != nil {
		t.Fatalf("end: %v", r)
	}
	if h.closedCount() != 2 {
		t.Fatalf("ending did not close the session (%d closes)", h.closedCount())
	}
	if names := named.Names(); len(names) != 1 || names[0] != "other" {
		t.Fatalf("kept %v", names)
	}
}

func TestNamedSessionRefusals(t *testing.T) {
	h := &closingHandler{fakeHandler: fakeHandler{started: make(chan string, 4)}}
	// Without a registry the transport refuses names.
	r := connect(t, h, Info{Name: "t"}, initWith("work"))
	if e, _ := r[0]["error"].(map[string]any); e == nil || e["code"] != float64(CodeInvalidParams) {
		t.Fatalf("no registry: %v", r)
	}
	info := Info{Name: "t", Named: &Named{}}
	r = connect(t, h, info, initWith("bad name!"))
	if e, _ := r[0]["error"].(map[string]any); e == nil {
		t.Fatalf("bad name accepted: %v", r)
	}
	r = connect(t, h, info, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, `{"jsonrpc":"2.0","id":2,"method":"`+EndSessionMethod+`"}`)
	if e, _ := r[1]["error"].(map[string]any); e == nil {
		t.Fatalf("ending an unnamed connection: %v", r)
	}
}

func TestNamedSessionIdleAndEviction(t *testing.T) {
	h := &closingHandler{}
	n := &Named{Idle: 20 * time.Millisecond, Max: 2}
	a, err := n.attach("a", "mcp", h)
	if err != nil {
		t.Fatal(err)
	}
	n.detach("a", false, h)
	deadline := time.Now().Add(2 * time.Second)
	for h.closedCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.closedCount() != 1 || h.closed[0] != a {
		t.Fatalf("idle session not closed: %d", h.closedCount())
	}

	n = &Named{Idle: time.Hour, Max: 2}
	h = &closingHandler{}
	if _, err := n.attach("x", "mcp", h); err != nil {
		t.Fatal(err)
	}
	y, _ := n.attach("y", "mcp", h)
	// Both in use: a third is refused.
	if _, err := n.attach("z", "mcp", h); err == nil {
		t.Fatal("a third session was let in while both are in use")
	}
	n.detach("y", false, h)
	if _, err := n.attach("z", "mcp", h); err != nil {
		t.Fatal(err)
	}
	if h.closedCount() != 1 || h.closed[0] != y {
		t.Fatalf("the idle session was not the one evicted: %d", h.closedCount())
	}
}
