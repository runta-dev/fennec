package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

type fakeHandler struct{ started chan string }

func (fakeHandler) Tools(*Session) []Tool {
	return []Tool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}, {Name: "block", InputSchema: json.RawMessage(`{"type":"object"}`)}}
}

func (h fakeHandler) Call(ctx context.Context, s *Session, name string, args json.RawMessage) Result {
	switch name {
	case "block":
		h.started <- s.AgentID
		<-ctx.Done()
		return Result{Text: "cancelled"}
	case "echo":
		var a struct {
			Sleep int    `json:"sleep"`
			Text  string `json:"text"`
		}
		_ = json.Unmarshal(args, &a)
		time.Sleep(time.Duration(a.Sleep) * time.Millisecond)
		return Result{Text: a.Text + "@" + s.AgentID, Images: []Image{{Data: "AA==", MIMEType: "image/png"}}}
	}
	return Result{Text: "?", IsError: true}
}

type harness struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Scanner
}

func start(t *testing.T) (*harness, fakeHandler) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := fakeHandler{started: make(chan string, 4)}
	go func() {
		_ = Serve(context.Background(), inR, outW, h, Info{Name: "t", Version: "0", Instructions: strings.Repeat("x", 3000)})
		_ = outW.Close()
	}()
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	t.Cleanup(func() { _ = inW.Close() })
	return &harness{t: t, in: inW, out: sc}, h
}

func (h *harness) send(s string) {
	h.t.Helper()
	if _, err := h.in.Write([]byte(s + "\n")); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) recv() map[string]any {
	h.t.Helper()
	if !h.out.Scan() {
		h.t.Fatal("no reply")
	}
	var m map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &m); err != nil {
		h.t.Fatal(err)
	}
	return m
}

func TestHandshakeNegotiatesVersionAndCapsInstructions(t *testing.T) {
	h, _ := start(t)
	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"claude code"}}}`)
	r := h.recv()["result"].(map[string]any)
	if r["protocolVersion"] != "2025-03-26" {
		t.Errorf("version %v", r["protocolVersion"])
	}
	if n := len(r["instructions"].(string)); n != 2048 {
		t.Errorf("instructions %d chars", n)
	}
	h.send(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if v := h.recv()["result"].(map[string]any)["protocolVersion"]; v != Versions[0] {
		t.Errorf("unknown version answered %v", v)
	}
}

func TestErrors(t *testing.T) {
	h, _ := start(t)
	cases := []struct {
		in   string
		code float64
		id   any
	}{
		{`{not json`, CodeParse, nil},
		{`{"jsonrpc":"2.0","id":3}`, CodeInvalidRequest, 3.0},
		{`{"jsonrpc":"2.0","id":4,"method":"nope"}`, CodeMethodNotFound, 4.0},
		{`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"missing"}}`, CodeInvalidParams, 5.0},
		{`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"echo","arguments":[1]}}`, CodeInvalidParams, 6.0},
	}
	for _, c := range cases {
		h.send(c.in)
		r := h.recv()
		e, _ := r["error"].(map[string]any)
		if e == nil || e["code"] != c.code || r["id"] != c.id {
			t.Errorf("%s: %v", c.in, r)
		}
	}
	h.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	h.send(`{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	if r := h.recv(); r["id"] != 7.0 {
		t.Errorf("a notification was answered: %v", r)
	}
}

func TestRepliesKeepRequestOrderAndCarryImages(t *testing.T) {
	h, _ := start(t)
	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"a b/c"}}}`)
	h.recv()
	h.send(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"echo","arguments":{"sleep":150,"text":"slow"}}}`)
	h.send(`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"echo","arguments":{"text":"fast"}}}`)
	first, second := h.recv(), h.recv()
	if first["id"] != 10.0 || second["id"] != 11.0 {
		t.Fatalf("order %v %v", first["id"], second["id"])
	}
	content := first["result"].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "slow@mcp-a-b-c-") {
		t.Errorf("agent id in %q", text)
	}
	if img := content[1].(map[string]any); img["type"] != "image" || img["mimeType"] != "image/png" {
		t.Errorf("image block %v", img)
	}
}

func TestCancelEndsTheCallWithoutReply(t *testing.T) {
	h, f := start(t)
	h.send(`{"jsonrpc":"2.0","id":20,"method":"tools/call","params":{"name":"block"}}`)
	<-f.started
	h.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":20}}`)
	h.send(`{"jsonrpc":"2.0","id":21,"method":"ping"}`)
	if r := h.recv(); r["id"] != 21.0 {
		t.Fatalf("cancelled call replied: %v", r)
	}
}

func TestAgentIDCleaning(t *testing.T) {
	if got := agentID("mcp-http", "", "abc/def"); got != "mcp-http-abc-def" {
		t.Errorf("fixed id %q", got)
	}
	if got := clean(strings.Repeat("é", 40)); len(got) != 32 {
		t.Errorf("cut to %d", len(got))
	}
}
