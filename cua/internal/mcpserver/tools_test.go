package mcpserver_test

// These tests run the real engine's tools behind the MCP server.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/engine"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
	"fennec/cua/internal/view"
)

type client struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Scanner
	id  int
}

func serve(t *testing.T, h mcpserver.Handler, info mcpserver.Info) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		_ = mcpserver.Serve(context.Background(), inR, outW, h, info)
		_ = outW.Close()
	}()
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	t.Cleanup(func() { _ = inW.Close() })
	return &client{t: t, in: inW, out: sc}
}

func (c *client) rpc(method string, params any) map[string]any {
	c.t.Helper()
	c.id++
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	if _, err := c.in.Write(append(data, '\n')); err != nil {
		c.t.Fatal(err)
	}
	if !c.out.Scan() {
		c.t.Fatal("no reply")
	}
	var m map[string]any
	if err := json.Unmarshal(c.out.Bytes(), &m); err != nil {
		c.t.Fatal(err)
	}
	return m
}

// tool calls a tool and returns its text, isError and the JSON-RPC error.
func (c *client) tool(name string, args any) (string, bool, map[string]any) {
	c.t.Helper()
	m := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	if e, ok := m["error"].(map[string]any); ok {
		return "", true, e
	}
	r := m["result"].(map[string]any)
	text, _ := r["content"].([]any)[0].(map[string]any)["text"].(string)
	return text, r["isError"] == true, nil
}

func newEngine(impl map[string]engine.Tool) *served {
	return &served{&engine.Engine{Turn: &sched.Turn{Busy: time.Second}, Stopper: &sched.Stopper{}, Impl: impl}}
}

// served reaches the engine directly: after js and js_reset it serves the
// engine tools by name, which agents reach only through the REPL, so these
// tests check the engine tools' schemas and answers over the transport.
type served struct{ *engine.Engine }

func (s *served) Tools(sess *mcpserver.Session) []mcpserver.Tool {
	return append(s.Engine.Tools(sess), s.EngineTools()...)
}

// toolList lists the engine tools with their schemas as the test server
// serves them after js and js_reset.
func toolList(t *testing.T, c *client) []map[string]any {
	t.Helper()
	var out []map[string]any
	for i, tl := range c.rpc("tools/list", nil)["result"].(map[string]any)["tools"].([]any) {
		m := tl.(map[string]any)
		if i < len(protocol.MCPTools) {
			if m["name"] != protocol.MCPTools[i] {
				t.Fatalf("tool %d is %v", i, m["name"])
			}
			continue
		}
		out = append(out, m)
	}
	return out
}

func schemaOf(t *testing.T, tools []map[string]any, name string) map[string]any {
	t.Helper()
	for _, tl := range tools {
		if tl["name"] == name {
			return tl["inputSchema"].(map[string]any)
		}
	}
	t.Fatalf("no tool %s", name)
	return nil
}

// prop follows a dotted path of properties; "[]" steps into array items.
func prop(s map[string]any, path string) map[string]any {
	for _, part := range strings.Split(path, ".") {
		if part == "[]" {
			items, _ := s["items"].(map[string]any)
			if items == nil {
				return nil
			}
			s = items
			continue
		}
		props, _ := s["properties"].(map[string]any)
		next, _ := props[part].(map[string]any)
		if next == nil {
			return nil
		}
		s = next
	}
	return s
}

func TestToolsListSchemas(t *testing.T) {
	c := serve(t, newEngine(nil), mcpserver.Info{Name: "fennec-cua"})
	tools := toolList(t, c)
	var names []string
	for _, tl := range tools {
		names = append(names, tl["name"].(string))
		s, _ := tl["inputSchema"].(map[string]any)
		if s == nil || s["type"] != "object" {
			t.Errorf("%s: schema %v", tl["name"], tl["inputSchema"])
		}
		if d, _ := tl["description"].(string); len(d) < 20 {
			t.Errorf("%s: description %q", tl["name"], d)
		}
	}
	if strings.Join(names, ",") != strings.Join(protocol.Tools, ",") || len(names) != 6 {
		t.Fatalf("tools %v", names)
	}
	if strings.Join(protocol.Tools, ",") != "observe,act,wait,run,flow,status" {
		t.Fatalf("protocol tools %v", protocol.Tools)
	}
	for _, p := range []string{"observe:scope", "act:steps.[].press", "wait:window_opens", "run:code", "flow:op"} {
		tool, path, _ := strings.Cut(p, ":")
		if prop(schemaOf(t, tools, tool), path) == nil {
			t.Errorf("%s schema lacks %s", tool, path)
		}
	}
}

func TestActDescriptionQtNote(t *testing.T) {
	c := serve(t, newEngine(nil), mcpserver.Info{})
	for _, tl := range toolList(t, c) {
		if tl["name"] != "act" {
			continue
		}
		d := tl["description"].(string)
		for _, want := range []string{"Qt", "no accessibility focus", "background typing", "search field", "another field", "scroll the list", "foreground"} {
			if !strings.Contains(d, want) {
				t.Errorf("act description lacks %q: %s", want, d)
			}
		}
		return
	}
	t.Fatal("no act tool")
}

func TestDescriptionsStateOutcomes(t *testing.T) {
	c := serve(t, newEngine(nil), mcpserver.Info{Instructions: engine.Instructions})
	instr := c.rpc("initialize", map[string]any{})["result"].(map[string]any)["instructions"].(string)
	if len(engine.Instructions) > 2048 || instr != engine.Instructions {
		t.Fatalf("instructions cut or changed (%d chars)", len(engine.Instructions))
	}
	var act, run string
	for _, tl := range toolList(t, c) {
		switch tl["name"] {
		case "act":
			act = tl["description"].(string)
		case "run":
			run = tl["description"].(string)
		}
	}
	// The instructions are one line pointing at the REPL; the outcomes are
	// documented on act (and in the REPL's documentation).
	if strings.Contains(instr, "\n") || !strings.Contains(instr, "cua") {
		t.Errorf("instructions %q", instr)
	}
	for name, text := range map[string]string{"act": act} {
		lower := strings.ToLower(text)
		for _, want := range []string{"refused", "declined", "busy", "confirm dialog", "sent nothing", "stopped", "unconfirmed", "typed", "partial trace", "may have taken effect"} {
			if !strings.Contains(lower, want) {
				t.Errorf("%s lacks %q", name, want)
			}
		}
	}
	if !strings.Contains(run, "may already have acted") {
		t.Errorf("run description: %s", run)
	}
}

func TestVerbCoverage(t *testing.T) {
	c := serve(t, newEngine(nil), mcpserver.Info{})
	tools := toolList(t, c)
	// Each desk verb shuttle answers on macOS, and where it lives now.
	verbs := map[string]string{
		"windows":        "observe:scope",
		"unanswered":     "observe:scope",
		"snapshot":       "observe:nodes",
		"find":           "observe:find",
		"read":           "observe:text",
		"at":             "observe:target",
		"shot":           "observe:image",
		"screen":         "observe:image",
		"screenshot":     "observe:region",
		"computer_state": "observe:vision",
		"look":           "observe:vision",
		"computer_scale": "observe:max_edge",
		"web":            "observe:web",
		"press":          "act:steps.[].press",
		"act":            "act:steps.[].set",
		"key":            "act:steps.[].key",
		"type":           "act:steps.[].type",
		"click":          "act:steps.[].click",
		"scroll":         "act:steps.[].scroll",
		"drag":           "act:steps.[].drag",
		"focus":          "act:steps.[].focus",
		"close":          "act:steps.[].close",
		"launch":         "act:steps.[].launch",
		"menu":           "act:steps.[].menu",
		"computer_act":   "act:steps.[].click",
		"gui_plan":       "act:steps.[].expect",
		"wait":           "wait:window_opens",
		"still":          "wait:still",
		"script_run":     "run:code",
		"flows":          "flow:op",
		"reindex":        "flow:query",
		"record_start":   "flow:apps",
		"record_note":    "flow:text",
		"record_stop":    "flow:params",
		"drafts":         "flow:name",
		"status":         "status:",
	}
	for verb, where := range verbs {
		tool, path, _ := strings.Cut(where, ":")
		s := schemaOf(t, tools, tool)
		if path != "" && prop(s, path) == nil {
			t.Errorf("shuttle verb %s: %s has no %s", verb, tool, path)
		}
	}
	for _, scope := range []string{protocol.ScopeDesktop, protocol.ScopeWindow, protocol.ScopeElement, protocol.ScopePoint} {
		if scope == "" {
			t.Error("empty scope")
		}
	}
	for _, op := range []string{protocol.FlowSave, protocol.FlowList, protocol.FlowShow, protocol.FlowRun, protocol.FlowDelete, protocol.FlowRecordStart, protocol.FlowRecordNote, protocol.FlowRecordStop} {
		if op == "" {
			t.Error("empty flow op")
		}
	}
}

type recordingObserver struct {
	mu   sync.Mutex
	reqs []protocol.ObserveRequest
}

func (o *recordingObserver) Observe(_ context.Context, req protocol.ObserveRequest) (*protocol.ObserveResult, error) {
	o.mu.Lock()
	o.reqs = append(o.reqs, req)
	o.mu.Unlock()
	return &protocol.ObserveResult{View: "v1"}, nil
}

func TestArgumentDefaults(t *testing.T) {
	obs := &recordingObserver{}
	eng := newEngine(map[string]engine.Tool{
		protocol.ToolObserve: engine.ObserveTool(obs),
		// No executor: a request that got past validation would crash.
		protocol.ToolAct: engine.ActTool(nil, nil, nil),
	})
	c := serve(t, eng, mcpserver.Info{})
	tools := toolList(t, c)
	if req, _ := schemaOf(t, tools, "observe")["required"].([]any); len(req) != 0 {
		t.Errorf("observe requires %v", req)
	}
	for _, args := range []any{nil, map[string]any{}} {
		if text, isErr, rpcErr := c.tool("observe", args); isErr || rpcErr != nil || !strings.Contains(text, "v1") {
			t.Errorf("observe %v: %s %v", args, text, rpcErr)
		}
	}
	if len(obs.reqs) != 2 || obs.reqs[0].Scope != "" || obs.reqs[1].Scope != "" {
		t.Errorf("observe got %+v; every option should take its default", obs.reqs)
	}
	for _, args := range []any{map[string]any{}, map[string]any{"steps": []any{map[string]any{"key": ""}}}, map[string]any{"steps": []any{map[string]any{}}}} {
		text, isErr, rpcErr := c.tool("act", args)
		if rpcErr != nil || !isErr || !strings.Contains(text, string(protocol.ErrInvalid)) {
			t.Errorf("act %v: %s %v", args, text, rpcErr)
		}
	}
	text, isErr, _ := c.tool("observe", map[string]any{"scop": "desktop"})
	if !isErr || !strings.Contains(text, string(protocol.ErrInvalid)) || len(obs.reqs) != 2 {
		t.Errorf("a misspelt argument: %s", text)
	}
}

// emptyDesk is a view.Source with no windows.
type emptyDesk struct{}

func (emptyDesk) Windows(context.Context) ([]view.RawWindow, error) { return nil, nil }
func (emptyDesk) Tree(context.Context, view.RawWindow, view.TreeOptions) (view.Tree, error) {
	return view.Tree{}, fmt.Errorf("no tree")
}
func (emptyDesk) Read(context.Context, view.Handle, bool, bool) (view.RawNode, bool, error) {
	return view.RawNode{}, false, fmt.Errorf("no element")
}
func (emptyDesk) At(context.Context, int, int) (*view.RawNode, []view.RawNode, *view.RawWindow, *view.RawWindow, error) {
	return nil, nil, nil, nil, nil
}
func (emptyDesk) Focused(context.Context, int) (*view.RawNode, error) { return nil, nil }

func TestNoFocusedWindowError(t *testing.T) {
	o := &view.Observer{Source: emptyDesk{}, Refs: view.NewRegistry(view.MaxRefs), Input: protocol.InputForeground}
	c := serve(t, newEngine(map[string]engine.Tool{protocol.ToolObserve: engine.ObserveTool(o)}), mcpserver.Info{})
	text, isErr, rpcErr := c.tool("observe", map[string]any{"scope": "desktop"})
	if isErr || rpcErr != nil {
		t.Fatalf("desktop: %s %v", text, rpcErr)
	}
	var res protocol.ObserveResult
	if err := json.Unmarshal([]byte(text), &res); err != nil || len(res.Windows) != 0 {
		t.Errorf("desktop: %s %v", text, err)
	}
	for _, args := range []any{map[string]any{"scope": "window"}, nil} {
		text, isErr, rpcErr = c.tool("observe", args)
		if !isErr || rpcErr != nil || !strings.Contains(text, "no focused window") {
			t.Errorf("window %v: %s %v", args, text, rpcErr)
		}
	}
}

func TestNoImagePathParameter(t *testing.T) {
	c := serve(t, newEngine(nil), mcpserver.Info{})
	tools := toolList(t, c)
	pathy := []string{"path", "file", "filename", "save", "save_to", "output", "out", "dir", "dest", "destination"}
	var walk func(tool, at string, s map[string]any)
	walk = func(tool, at string, s map[string]any) {
		props, _ := s["properties"].(map[string]any)
		for name, sub := range props {
			for _, p := range pathy {
				if strings.EqualFold(name, p) {
					t.Errorf("%s takes %s%s: a caller could choose where a capture is written", tool, at, name)
				}
			}
			if m, ok := sub.(map[string]any); ok {
				walk(tool, at+name+".", m)
			}
		}
		if items, ok := s["items"].(map[string]any); ok {
			walk(tool, at+"[].", items)
		}
	}
	for _, tool := range []string{"observe", "act", "wait", "status"} {
		walk(tool, "", schemaOf(t, tools, tool))
	}
	// Images travel inline in the reply.
	if prop(schemaOf(t, tools, "observe"), "image") == nil {
		t.Error("observe has no image option")
	}
}

func TestSessionIsCaller(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	eng := newEngine(map[string]engine.Tool{protocol.ToolObserve: func(_ context.Context, call *engine.Call, _ json.RawMessage) (any, *protocol.Error) {
		mu.Lock()
		seen = append(seen, call.Session.AgentID)
		mu.Unlock()
		return map[string]string{"agent": call.Session.AgentID}, nil
	}})
	a := serve(t, eng, mcpserver.Info{IDPrefix: "mcp"})
	b := serve(t, eng, mcpserver.Info{IDPrefix: "mcp"})
	for _, c := range []*client{a, b} {
		c.rpc("initialize", map[string]any{"clientInfo": map[string]any{"name": "same-client"}})
		c.tool("observe", nil)
		c.tool("observe", nil)
	}
	if len(seen) != 4 || seen[0] != seen[1] || seen[2] != seen[3] || seen[0] == seen[2] {
		t.Errorf("callers %v", seen)
	}
	// HTTP sessions are callers of their own too.
	srv, err := mcpserver.NewHTTP(eng, mcpserver.Info{}, mcpserver.HTTPConfig{Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	post := func(sid, body string) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+mcpserver.HTTPPath, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer t")
		if sid != "" {
			req.Header.Set(mcpserver.SessionHeader, sid)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}
	var sids []string
	for i := 0; i < 2; i++ {
		sid := post("", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"same-client"}}}`).Header.Get(mcpserver.SessionHeader)
		sids = append(sids, sid)
		post(sid, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"observe"}}`)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 6 || seen[4] != "mcp-http-"+sids[0] || seen[5] != "mcp-http-"+sids[1] || seen[4] == seen[5] {
		t.Errorf("HTTP callers %v for sessions %v", seen[4:], sids)
	}
}

func TestNoToolReachesSafety(t *testing.T) {
	eng := newEngine(nil)
	c := serve(t, eng, mcpserver.Info{})
	for _, tl := range toolList(t, c) {
		n := strings.ToLower(tl["name"].(string))
		if strings.Contains(n, "stop") || strings.Contains(n, "resume") || strings.Contains(n, "control") {
			t.Errorf("tool %s", n)
		}
	}
	tryAll := func() {
		for _, name := range []string{"stop", "resume", "../stop", "../resume", "status/../stop", "/stop", "control", "STOP", "stop\x00"} {
			if _, _, rpcErr := c.tool(name, map[string]any{}); rpcErr == nil {
				t.Errorf("tool %q answered", name)
			}
		}
		c.tool("status", map[string]any{"op": "stop"})
		c.tool("status", map[string]any{"op": "resume"})
		c.tool("flow", map[string]any{"op": "stop"})
		c.tool("flow", map[string]any{"op": "resume"})
		c.tool("act", map[string]any{"steps": []any{map[string]any{"menu": "fennec-cua > Stop"}}})
	}
	tryAll()
	if eng.Stopper.Stopped() {
		t.Fatal("an agent stopped fennec-cua")
	}
	eng.Stopper.Stop()
	tryAll()
	if !eng.Stopper.Stopped() {
		t.Fatal("an agent resumed fennec-cua")
	}
}
