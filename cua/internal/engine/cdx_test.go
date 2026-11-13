package engine_test

// The Codex parity tests (docs/codex-parity.md, section 9). They run the js
// REPL over MCP against a fake desktop: nothing here sends input to the host.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/mcpclient"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
	"fennec/cua/internal/view"
)

// A 1x1 PNG.
var pngData = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

func sp(s string) *string { return &s }

// world is the fake desktop the engine tools answer from.
type world struct {
	mu      sync.Mutex
	windows []protocol.Window
	trees   map[string][]protocol.Node
	apps    []protocol.AppInfo
	acts    []map[string]any // each act request as sent
	observe []map[string]any
	// fail answers an act step with this result when set.
	fail     func(step map[string]any) *protocol.ActResult
	launched func(w *world)
	pb       *fakePB
}

type fakePB struct {
	mu   sync.Mutex
	text string
	sets []string
}

func (p *fakePB) Text(context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.text, nil
}

func (p *fakePB) SetText(_ context.Context, s string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.text = s
	p.sets = append(p.sets, s)
	return nil
}

func fixtureTree() []protocol.Node {
	return []protocol.Node{
		{Ref: "w1", Role: "window", RoleDesc: "standard window", Name: "Runta CUA Fixture", Actions: []string{"Raise"}},
		{Ref: "e1", Role: "text", RoleDesc: "text", Value: sp("Runta CUA Fixture"), Parent: "w1", Depth: 1},
		{Ref: "e20", Role: "group", RoleDesc: "group", Parent: "w1", Depth: 1},
		{Ref: "e2", Role: "button", RoleDesc: "button", Name: "Start trial (evaluator)", Parent: "e20", Depth: 2},
		{Ref: "e3", Role: "text", RoleDesc: "text", Value: sp("Evaluator: press Start when ready"), Parent: "w1", Depth: 1},
		{Ref: "e4", Role: "button", RoleDesc: "close button", Parent: "w1", Depth: 1},
		{Ref: "e5", Role: "button", RoleDesc: "zoom button", States: []string{"disabled"}, Parent: "w1", Depth: 1},
		{Ref: "e6", Role: "button", RoleDesc: "minimize button", Parent: "w1", Depth: 1},
		{Ref: "e7", Role: "menubar", RoleDesc: "menu bar"},
		{Ref: "e8", Role: "menubaritem", RoleDesc: "menu bar item", Name: "Runta CUA Fixture", Parent: "e7", Depth: 1},
		{Ref: "e9", Role: "menu", RoleDesc: "menu", Parent: "e8", Depth: 2},
		{Ref: "e10", Role: "menuitem", RoleDesc: "menu item", Name: "About", Parent: "e9", Depth: 3},
	}
}

const fixtureState = "Window: \"Runta CUA Fixture\", App: Runta CUA Fixture.\n" +
	"0 standard window Runta CUA Fixture, Secondary Actions: Raise\n" +
	"\t1 text Runta CUA Fixture\n" +
	"\t2 button Start trial (evaluator)\n" +
	"\t3 text Evaluator: press Start when ready\n" +
	"\t4 close button\n" +
	"\t5 zoom button (disabled)\n" +
	"\t6 minimize button\n" +
	"7 menu bar\n" +
	"\t8 Runta CUA Fixture\n" +
	"\n" +
	"The focused UI element is 0 standard window Runta CUA Fixture, Secondary Actions: Raise"

func newWorld() *world {
	return &world{
		windows: []protocol.Window{{Ref: "w1", App: "Runta CUA Fixture", Title: "Runta CUA Fixture", Role: "window", PID: 42, Active: true}},
		trees:   map[string][]protocol.Node{"w1": fixtureTree()},
		apps: []protocol.AppInfo{
			{Name: "Runta CUA Fixture", BundleID: "dev.runta.fixture", Path: "/Applications/Runta CUA Fixture.app", PID: 42, Running: true},
			{Name: "TextEdit", BundleID: "com.apple.TextEdit", Path: "/System/Applications/TextEdit.app", LastUsed: "2026-11-01", UseCount: 3},
		},
		pb: &fakePB{text: "before"},
	}
}

func (w *world) engine() *engine.Engine {
	e := &engine.Engine{Turn: &sched.Turn{Busy: 300 * time.Millisecond}, Stopper: &sched.Stopper{}}
	e.Impl = map[string]engine.Tool{
		protocol.ToolObserve: func(_ context.Context, _ *engine.Call, args json.RawMessage) (any, *protocol.Error) {
			var req protocol.ObserveRequest
			if perr := engine.Decode(args, &req); perr != nil {
				return nil, perr
			}
			var m map[string]any
			_ = json.Unmarshal(args, &m)
			w.mu.Lock()
			defer w.mu.Unlock()
			w.observe = append(w.observe, m)
			res := &protocol.ObserveResult{View: "v1", Windows: append([]protocol.Window(nil), w.windows...)}
			if req.Scope == protocol.ScopeDesktop {
				return res, nil
			}
			for i := range w.windows {
				if w.windows[i].Ref == req.Window {
					win := w.windows[i]
					res.Window = &win
				}
			}
			if res.Window == nil {
				return nil, &protocol.Error{Code: protocol.ErrWindowNotFound, Message: "no window " + req.Window}
			}
			if req.Image == "window" {
				res.Image = &protocol.Image{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(pngData), Width: 1, Height: 1, Scale: 1}
			}
			if req.Detail {
				res.Nodes = append([]protocol.Node(nil), w.trees[req.Window]...)
			}
			return res, nil
		},
		protocol.ToolAct: func(ctx context.Context, call *engine.Call, args json.RawMessage) (any, *protocol.Error) {
			var m map[string]any
			_ = json.Unmarshal(args, &m)
			release, err := call.Turn(ctx)
			if err != nil {
				return nil, engine.TurnError(err)
			}
			defer release()
			w.mu.Lock()
			w.acts = append(w.acts, m)
			fail, launched := w.fail, w.launched
			w.mu.Unlock()
			steps, _ := m["steps"].([]any)
			res := protocol.ActResult{OK: true, View: "v2"}
			for i, s := range steps {
				step, _ := s.(map[string]any)
				if fail != nil {
					if r := fail(step); r != nil {
						return r, nil
					}
				}
				if _, ok := step["launch"]; ok && launched != nil {
					w.mu.Lock()
					launched(w)
					w.mu.Unlock()
				}
				res.Steps = append(res.Steps, protocol.StepResult{Index: i, OK: true, Status: protocol.StatusConfirmed})
			}
			return res, nil
		},
		protocol.ToolWait: func(context.Context, *engine.Call, json.RawMessage) (any, *protocol.Error) {
			return protocol.WaitResult{Met: true}, nil
		},
		protocol.ToolFlow: func(_ context.Context, _ *engine.Call, args json.RawMessage) (any, *protocol.Error) {
			var m map[string]any
			_ = json.Unmarshal(args, &m)
			return map[string]any{"op": m["op"]}, nil
		},
	}
	e.Impl[protocol.ToolApps] = e.AppsTool(func(context.Context) ([]protocol.AppInfo, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return append([]protocol.AppInfo(nil), w.apps...), nil
	})
	engine.PasteSettle = time.Millisecond
	e.Impl[protocol.ToolPaste] = e.PasteTool(w.pb)
	return e
}

func (w *world) actSteps() []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []map[string]any
	for _, a := range w.acts {
		steps, _ := a["steps"].([]any)
		for _, s := range steps {
			m, _ := s.(map[string]any)
			out = append(out, m)
		}
	}
	return out
}

// mcp is one MCP session against an engine.
type mcp struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
	id  int
}

func session(t *testing.T, h mcpserver.Handler) *mcp {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		_ = mcpserver.Serve(context.Background(), inR, outW, h, mcpserver.Info{Name: "fennec-cua", Instructions: engine.Instructions})
		_ = outW.Close()
	}()
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	t.Cleanup(func() { _ = inW.Close() })
	c := &mcp{t: t, in: inW, out: sc}
	c.rpc("initialize", map[string]any{"protocolVersion": "2025-06-18"})
	return c
}

func (c *mcp) rpc(method string, params any) map[string]any {
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

// block is one content block of a tool result.
type block struct {
	Type, Text, Data, MIME string
}

type result struct {
	blocks  []block
	isError bool
	raw     map[string]any
}

// output is the result's text after the framing and the guide.
func (r result) output() string {
	var parts []string
	for i, b := range r.blocks {
		if i == 0 || b.Type != "text" || strings.HasPrefix(b.Text, "# fennec-cua") {
			continue
		}
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n")
}

func (r result) all() string {
	var parts []string
	for _, b := range r.blocks {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n")
}

func (c *mcp) call(name string, args any) result {
	c.t.Helper()
	m := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	res, ok := m["result"].(map[string]any)
	if !ok {
		c.t.Fatalf("%s: %v", name, m)
	}
	out := result{raw: res}
	out.isError, _ = res["isError"].(bool)
	for _, b := range res["content"].([]any) {
		bm := b.(map[string]any)
		blk := block{}
		blk.Type, _ = bm["type"].(string)
		blk.Text, _ = bm["text"].(string)
		blk.Data, _ = bm["data"].(string)
		blk.MIME, _ = bm["mimeType"].(string)
		out.blocks = append(out.blocks, blk)
	}
	return out
}

func (c *mcp) js(code string) result {
	c.t.Helper()
	return c.call("js", map[string]any{"code": code, "title": "Testing"})
}

// ok runs code and fails the test on an error result.
func (c *mcp) ok(code string) string {
	c.t.Helper()
	r := c.js(code)
	if r.isError {
		c.t.Fatalf("js %q failed:\n%s", code, r.all())
	}
	return r.output()
}

// CDX-01: tools/list is exactly js and js_reset, with section 1's schemas.
func TestCDX01ToolsList(t *testing.T) {
	c := session(t, newWorld().engine())
	tools := c.rpc("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "js,js_reset" {
		t.Fatalf("tools %v", names)
	}
	props := tools[0].(map[string]any)["inputSchema"].(map[string]any)["properties"].(map[string]any)
	for _, p := range []string{"code", "title", "completion_summary"} {
		if props[p] == nil {
			t.Errorf("js schema lacks %s", p)
		}
	}
	if req := tools[0].(map[string]any)["inputSchema"].(map[string]any)["required"]; req != nil {
		if l, _ := req.([]any); len(l) > 0 {
			t.Errorf("js requires %v; every argument is optional", req)
		}
	}
	reset := tools[1].(map[string]any)["inputSchema"].(map[string]any)
	if p, _ := reset["properties"].(map[string]any); len(p) != 0 {
		t.Errorf("js_reset takes arguments: %v", p)
	}
	// A plain session reaches no engine tool by name: act, observe and the
	// rest are unknown tools (only a script plugin's run token opens them);
	// turn_ended, the host's notice, answers without being listed.
	for _, name := range []string{"act", "observe", "wait", "run", "flow", "status", "apps", "paste"} {
		m := c.rpc("tools/call", map[string]any{"name": name, "arguments": map[string]any{"steps": []any{map[string]any{"key": "cmd+q"}}}})
		e, _ := m["error"].(map[string]any)
		if e == nil || e["code"] != float64(mcpserver.CodeInvalidParams) || e["message"] != "unknown tool: "+name {
			t.Errorf("%s by name: %v", name, m)
		}
	}
	if r := c.call("turn_ended", map[string]any{}); r.isError {
		t.Errorf("turn_ended: %v", r.all())
	}
	instr := c.rpc("initialize", map[string]any{})["result"].(map[string]any)["instructions"].(string)
	if strings.Contains(instr, "\n") || !strings.Contains(instr, "cua") {
		t.Errorf("instructions %q", instr)
	}
}

var framingRe = regexp.MustCompile(`^Wall time: \d+\.\d{4} seconds\nOutput:$`)

// CDX-02: every js result starts with the wall-time framing.
func TestCDX02Framing(t *testing.T) {
	c := session(t, newWorld().engine())
	for _, code := range []string{`nodeRepl.write("hi")`, `throw new Error("x")`, `1 +`} {
		r := c.js(code)
		if len(r.blocks) == 0 || !framingRe.MatchString(r.blocks[0].Text) {
			t.Errorf("%q: first block %q", code, r.blocks[0].Text)
		}
	}
	if out := c.ok(`nodeRepl.write("hi")`); out != "hi" {
		t.Errorf("output %q", out)
	}
}

// CDX-03: bindings persist across calls, and top-level await works.
func TestCDX03Persistence(t *testing.T) {
	c := session(t, newWorld().engine())
	c.ok(`var a = 1; let b = 2; const c = 3; function f(x) { return x * 10 } class K { v() { return 7 } }`)
	c.ok(`let d = await new Promise(r => setTimeout(() => r(4), 5)); const {e, g: [h]} = {e: 5, g: [6]}`)
	if out := c.ok(`nodeRepl.write([a, b, c, f(1), new K().v(), d, e, h].join(","))`); out != "1,2,3,10,7,4,5,6" {
		t.Errorf("bindings %q", out)
	}
	// Declaring a name again is allowed, as in a REPL.
	c.ok(`let b = 20; const c = 30`)
	if out := c.ok(`nodeRepl.write(String(b + c))`); out != "50" {
		t.Errorf("redeclared %q", out)
	}
	// A last expression's value is not echoed (section 2); a parenthesized
	// last expression still runs.
	if out := c.ok(`1 + 1`); out != "" {
		t.Errorf("value echoed %q", out)
	}
	c.ok(`({x: 1})`)
	c.ok(`(f)(2)`)
	if out := c.ok(`nodeRepl.write("w"); 5`); out != "w" {
		t.Errorf("output %q", out)
	}
}

// CDX-04: js_reset drops the bindings and shows the documentation again;
// it changes nothing on the desktop.
func TestCDX04Reset(t *testing.T) {
	w := newWorld()
	c := session(t, w.engine())
	c.ok(`var keep = 1`)
	acts := len(w.actSteps())
	if r := c.call("js_reset", map[string]any{}); r.isError {
		t.Fatal(r.all())
	}
	if len(w.actSteps()) != acts {
		t.Error("reset acted on the desktop")
	}
	r := c.js(`nodeRepl.write(typeof keep)`)
	if r.isError || !strings.Contains(r.all(), "# fennec-cua") || r.output() != "undefined" {
		t.Errorf("after reset: %v %q", r.isError, r.all())
	}
}

// CDX-05: an uncaught error ends the call with Error: and isError; what
// was written stays, and the session stays usable with its bindings.
func TestCDX05Errors(t *testing.T) {
	c := session(t, newWorld().engine())
	r := c.js(`var before = 1; nodeRepl.write("partial"); throw new Error("boom")`)
	if !r.isError {
		t.Fatal("no isError")
	}
	last := r.blocks[len(r.blocks)-1].Text
	if !strings.HasPrefix(last, "Error: boom") || !strings.Contains(r.output(), "partial") {
		t.Errorf("error output %q", r.all())
	}
	r = c.js(`null.x`)
	if !r.isError || !strings.Contains(r.blocks[len(r.blocks)-1].Text, "TypeError") {
		t.Errorf("type error %q", r.all())
	}
	r = c.js(`let = = 1`)
	if !r.isError || !strings.Contains(r.all(), "SyntaxError") {
		t.Errorf("syntax error %q", r.all())
	}
	if out := c.ok(`nodeRepl.write(String(before))`); out != "1" {
		t.Errorf("after errors %q", out)
	}
}

// CDX-06: a call with only completion_summary is acknowledged.
func TestCDX06CompletionSummary(t *testing.T) {
	c := session(t, newWorld().engine())
	r := c.call("js", map[string]any{"completion_summary": "Filled the trial form"})
	if r.isError || len(r.blocks) != 1 || r.blocks[0].Text != "Completion summary received." {
		t.Errorf("summary: %v", r.all())
	}
}

// CDX-07: the documentation comes once per session, with section 4's
// topics in order.
func TestCDX07Documentation(t *testing.T) {
	c := session(t, newWorld().engine())
	first := c.js(`1`)
	doc := ""
	for _, b := range first.blocks {
		if strings.HasPrefix(b.Text, "# fennec-cua") {
			doc = b.Text
		}
	}
	if doc == "" || first.blocks[1].Text != doc {
		t.Fatalf("first call has no documentation right after the framing: %q", first.all())
	}
	at := 0
	for _, topic := range []string{"Use it for", "## API", "cua.getApp(target", "## Opening an app", "## Working loop", "## Output", "## Keys", "## Confirmations", "## fennec"} {
		i := strings.Index(doc[at:], topic)
		if i < 0 {
			t.Errorf("documentation lacks %q after offset %d", topic, at)
			continue
		}
		at += i
	}
	if strings.Contains(c.js(`2`).all(), "# fennec-cua") {
		t.Error("the documentation came twice")
	}
	if c2 := session(t, newWorld().engine()); !strings.Contains(c2.js(`1`).all(), "# fennec-cua") {
		t.Error("a new session got no documentation")
	}
}

// CDX-08: nodeRepl.write and emitImage; file URLs are refused.
func TestCDX08NodeRepl(t *testing.T) {
	c := session(t, newWorld().engine())
	b64 := base64.StdEncoding.EncodeToString(pngData)
	c.ok(`var atobBytes = [` + byteList(pngData) + `]`)
	r := c.js(`nodeRepl.write("one"); nodeRepl.write({a: 1});
await nodeRepl.emitImage("data:image/png;base64,` + b64 + `");
const bytes = Uint8Array.from(atobBytes);
await nodeRepl.emitImage(bytes);
await nodeRepl.emitImage({bytes, mimeType: "image/png"});
nodeRepl.write("two")`)
	if r.isError {
		t.Fatal(r.all())
	}
	var kinds []string
	for _, b := range r.blocks[1:] {
		if b.Type == "image" {
			kinds = append(kinds, "image")
			if b.MIME != "image/png" || b.Data != b64 {
				t.Errorf("image %s %q", b.MIME, b.Data)
			}
			continue
		}
		kinds = append(kinds, b.Text)
	}
	if got := strings.Join(kinds, "|"); got != "one|{\n  \"a\": 1\n}|image|image|image|two" {
		t.Errorf("parts %q", got)
	}
	r = c.js(`await nodeRepl.emitImage("file:///etc/hosts")`)
	if !r.isError || !strings.Contains(r.all(), "file URLs are refused") {
		t.Errorf("file URL: %q", r.all())
	}
}

func byteList(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = itoa(int(x))
	}
	return strings.Join(parts, ",")
}

func itoa(n int) string { return strconv.Itoa(n) }

// CDX-09: self-displaying calls display once; emit false keeps them quiet.
func TestCDX09SelfDisplay(t *testing.T) {
	c := session(t, newWorld().engine())
	out := c.ok(`var app = await cua.getApp("Runta CUA Fixture")`)
	if out != fixtureState {
		t.Fatalf("getApp display:\n%s", out)
	}
	if out := c.ok(`await app.getAXState({emit: false, disableDiffing: true})`); out != "" {
		t.Errorf("emit false displayed %q", out)
	}
	if out := c.ok(`await app.getAXState({disableDiffing: true})`); strings.Count(out, "Window:") != 1 {
		t.Errorf("displayed %d times:\n%s", strings.Count(out, "Window:"), out)
	}
	if out := c.ok(`await cua.listApps({emit: false}); await cua.getState({emit: false})`); out != "" {
		t.Errorf("quiet inventory displayed %q", out)
	}
	r := c.js(`await app.getScreenshot()`)
	images := 0
	for _, b := range r.blocks {
		if b.Type == "image" {
			images++
		}
	}
	if r.isError || images != 1 {
		t.Errorf("screenshot shown %d times: %q", images, r.all())
	}
	r = c.js(`const shot = await app.getScreenshot({emit: false}); nodeRepl.write(shot.length + " " + shot[1])`)
	if r.isError || r.output() != itoa(len(pngData))+" 80" {
		t.Errorf("quiet screenshot %q", r.all())
	}
}

// CDX-10: the full state, byte for byte.
func TestCDX10FullState(t *testing.T) {
	c := session(t, newWorld().engine())
	c.ok(`var app = await cua.getApp("Runta CUA Fixture")`)
	if out := c.ok(`await app.getAXState({disableDiffing: true})`); out != fixtureState {
		t.Errorf("state:\n%s\nwant:\n%s", out, fixtureState)
	}
}

// CDX-11: the diff: removed ranges, changed and added lines with stable
// and fresh ids, and the focused line.
func TestCDX11Diff(t *testing.T) {
	w := newWorld()
	c := session(t, w.engine())
	c.ok(`var app = await cua.getApp("Runta CUA Fixture")`)
	w.mu.Lock()
	tree := fixtureTree()
	tree[1].Value = sp("Runta CUA Fixture Ready Runtime name")
	next := []protocol.Node{tree[0], tree[1]}
	next = append(next,
		protocol.Node{Ref: "e30", Role: "textbox", RoleDesc: "text field", Name: "Runtime name", Description: "Runtime name", Value: sp("old-runtime"), States: []string{"settable", "focused"}, Parent: "w1", Depth: 1},
		protocol.Node{Ref: "e31", Role: "text", RoleDesc: "text", Value: sp("Region"), Parent: "w1", Depth: 1},
	)
	next = append(next, tree[5:]...)
	w.trees["w1"] = next
	w.mu.Unlock()
	want := "The following is a diff from the previous accessibility tree for Window: \"Runta CUA Fixture\" with ~ and + representing changed and added elements, respectively. Removed elements are summarized by ID range.\n" +
		"Removed element IDs: 2-3\n" +
		"~\t1 text Runta CUA Fixture Ready Runtime name\n" +
		"+\t\t9 text field (settable) Description: Runtime name, Value: old-runtime\n" +
		"+\t\t10 text Region\n" +
		"The focused UI element is 9 text field (settable) Description: Runtime name, Value: old-runtime"
	if out := c.ok(`await app.getAXState()`); out != want {
		t.Errorf("diff:\n%s\nwant:\n%s", out, want)
	}
	// The new ids address the new elements.
	c.ok(`await app.setValue(9, "runta-27611")`)
	steps := w.actSteps()
	if s := steps[len(steps)-1]; s["set"].(map[string]any)["ref"] != "e30" || s["value"] != "runta-27611" {
		t.Errorf("set step %v", s)
	}
}

// CDX-12: disableDiffing gives the full state with the same ids; another
// front window gives a full state from id 0; no change says so in one line.
func TestCDX12FullAndUnchanged(t *testing.T) {
	w := newWorld()
	c := session(t, w.engine())
	c.ok(`var app = await cua.getApp("Runta CUA Fixture")`)
	out := c.ok(`await app.getAXState()`)
	if out != `The accessibility tree for Window: "Runta CUA Fixture" has not changed since the previous read.` {
		t.Errorf("unchanged: %q", out)
	}
	if out := c.ok(`await app.getAXState({disableDiffing: true})`); out != fixtureState {
		t.Errorf("full again:\n%s", out)
	}
	w.mu.Lock()
	w.windows = []protocol.Window{{Ref: "w2", App: "Runta CUA Fixture", Title: "Settings", Role: "window", PID: 42, Active: true}, {Ref: "w1", App: "Runta CUA Fixture", Title: "Runta CUA Fixture", Role: "window", PID: 42}}
	w.trees["w2"] = []protocol.Node{
		{Ref: "w2", Role: "window", RoleDesc: "standard window", Name: "Settings"},
		{Ref: "e40", Role: "checkbox", RoleDesc: "checkbox", Name: "Dark", Value: sp("1"), States: []string{"checked", "focused"}, Parent: "w2", Depth: 1},
	}
	w.mu.Unlock()
	want := "Window: \"Settings\", App: Runta CUA Fixture.\n0 standard window Settings\n\t1 checkbox (checked) Dark\n\nThe focused UI element is 1 checkbox (checked) Dark"
	if out := c.ok(`await app.getAXState()`); out != want {
		t.Errorf("window change:\n%s\nwant:\n%s", out, want)
	}
}

func lastStep(w *world) map[string]any {
	s := w.actSteps()
	if len(s) == 0 {
		return nil
	}
	return s[len(s)-1]
}

func stepJSON(s map[string]any) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// CDX-13: click by id and by screenshot point, with count, button and key.
func TestCDX13Click(t *testing.T) {
	w := newWorld()
	c := session(t, w.engine())
	c.ok(`var app = await cua.getApp("Runta CUA Fixture")`)
	cases := map[string]string{
		`await app.click(2)`:                                        `{"press":{"ref":"e2"}}`,
		`await app.click([120, 45])`:                                `{"click":{"point":{"space":"window","x":120,"y":45}}}`,
		`await app.click(2, {clickCount: 2})`:                       `{"click":{"ref":"e2"},"count":2}`,
		`await app.click(2, {mouseButton: "r"})`:                    `{"button":"right","click":{"ref":"e2"}}`,
		`await app.click([5, 6], {key: "shift", mouseButton: "m"})`: `{"button":"middle","click":{"point":{"space":"window","x":5,"y":6}},"mods":["shift"]}`,
	}
	for code, want := range cases {
		c.ok(code)
		if got := stepJSON(lastStep(w)); got != want {
			t.Errorf("%s: step %s, want %s", code, got, want)
		}
	}
	w.mu.Lock()
	win := w.acts[len(w.acts)-1]["window"]
	w.mu.Unlock()
	if win != "w1" {
		t.Errorf("act window %v", win)
	}
	if r := c.js(`await app.click(2, {mouseButton: "side"})`); !r.isError {
		t.Error("a bad button was accepted")
	}
	// TODO(live): CDX-13 in the VM against CUAFixture: the click lands, a
	// double click opens, a right click shows the context menu.
}

// CDX-14: setValue, selectText and performSecondaryAction.
func TestCDX14ValuesAndActions(t *testing.T) {
	w := newWorld()
	w.trees["w1"] = append(fixtureTree(), protocol.Node{Ref: "e50", Role: "textbox", RoleDesc: "text area", Value: sp("one two one three"), States: []string{"settable"}, Actions: []string{"Show Menu"}, Parent: "w1", Depth: 1})
	c := session(t, w.engine())
	out := c.ok(`var app = await cua.getApp("Runta CUA Fixture")`)
	if !strings.Contains(out, "\t9 text area (settable) Value: one two one three, Secondary Actions: Show Menu") {
		t.Fatalf("state:\n%s", out)
	}
	cases := map[string]string{
		`await app.setValue(9, 42)`:                                                       `{"set":{"ref":"e50"},"value":"42"}`,
		`await app.selectText(9, "two")`:                                                  `{"range":[4,3],"select_text":{"ref":"e50"}}`,
		`await app.selectText(9, "one", {prefix: "two "})`:                                `{"range":[8,3],"select_text":{"ref":"e50"}}`,
		`await app.selectText(9, "one", {suffix: " two", selectionType: "cursor_after"})`: `{"range":[3,0],"select_text":{"ref":"e50"}}`,
		`await app.selectText(9, "three", {selectionType: "cursor_before"})`:              `{"range":[12,0],"select_text":{"ref":"e50"}}`,
		`await app.performSecondaryAction(9, "show menu")`:                                `{"action":"Show Menu","perform":{"ref":"e50"}}`,
	}
	for code, want := range cases {
		c.ok(code)
		if got := stepJSON(lastStep(w)); got != want {
			t.Errorf("%s: step %s, want %s", code, got, want)
		}
	}
	n := len(w.actSteps())
	for code, msg := range map[string]string{
		`await app.performSecondaryAction(9, "Raise")`: "it lists: Show Menu",
		`await app.performSecondaryAction(4, "Press")`: "it lists none",
		`await app.selectText(9, "one")`:               "appears 2 times",
		`await app.selectText(9, "four")`:              "is not in the value",
	} {
		r := c.js(code)
		if !r.isError || !strings.Contains(r.all(), msg) {
			t.Errorf("%s: %q", code, r.all())
		}
	}
	if len(w.actSteps()) != n {
		t.Error("a refused call acted")
	}
	// TODO(live): CDX-14 in the VM: the value, selection and action land.
}

// CDX-15: pressKey takes xdotool syntax, typeText types, paste restores
// the pasteboard.
func TestCDX15Keys(t *testing.T) {
	w := newWorld()
	c := session(t, w.engine())
	c.ok(`var app = await cua.getApp("Runta CUA Fixture")`)
	c.ok(`await app.pressKey("super+c")`)
	if s := lastStep(w); s["key"] != "super+c" {
		t.Errorf("key step %v", s)
	}
	c.ok(`await app.typeText("hello")`)
	if s := lastStep(w); s["type"] != "hello" {
		t.Errorf("type step %v", s)
	}
	c.ok(`await app.paste("pasted text")`)
	if s := lastStep(w); s["key"] != "cmd+v" {
		t.Errorf("paste key %v", s)
	}
	w.pb.mu.Lock()
	sets, text := w.pb.sets, w.pb.text
	w.pb.mu.Unlock()
	if strings.Join(sets, "|") != "pasted text|before" || text != "before" {
		t.Errorf("pasteboard %q now %q", sets, text)
	}
	// TODO(live): CDX-15 in the VM: the xdotool table reaches the app.
}

// CDX-16: scroll by element and point, in pages and pixels; drag.
func TestCDX16ScrollDrag(t *testing.T) {
	w := newWorld()
	c := session(t, w.engine())
	c.ok(`var app = await cua.getApp("Runta CUA Fixture")`)
	cases := map[string]string{
		`await app.scroll(2, "d")`:                   `{"amount":5,"direction":"down","scroll":{"ref":"e2"}}`,
		`await app.scroll([10, 20], "up", 2)`:        `{"amount":10,"direction":"up","scroll":{"point":{"space":"window","x":10,"y":20}}}`,
		`await app.scroll(2, "left", {pixels: 200})`: `{"amount":10,"direction":"left","scroll":{"ref":"e2"}}`,
		`await app.drag([1, 2], [30, 40])`:           `{"drag":{"point":{"space":"window","x":1,"y":2}},"to":{"point":{"space":"window","x":30,"y":40}}}`,
	}
	for code, want := range cases {
		c.ok(code)
		if got := stepJSON(lastStep(w)); got != want {
			t.Errorf("%s: step %s, want %s", code, got, want)
		}
	}
	if r := c.js(`await app.scroll(2, "sideways")`); !r.isError {
		t.Error("a bad direction was accepted")
	}
	// TODO(live): CDX-16 in the VM: the list scrolls, the drag moves.
}

// CDX-17: getApp by name, path and bundle id; a stopped app is launched in
// the background; an unknown name says to use a bundle id.
func TestCDX17GetApp(t *testing.T) {
	for _, target := range []string{"runta cua fixture", "dev.runta.fixture", "/Applications/Runta CUA Fixture.app"} {
		c := session(t, newWorld().engine())
		if out := c.ok(`var app = await cua.getApp(` + quote(target) + `)`); out != fixtureState {
			t.Errorf("%s: %q", target, out)
		}
	}
	w := newWorld()
	w.launched = func(w *world) {
		w.apps[1].Running, w.apps[1].PID = true, 77
		w.windows = append(w.windows, protocol.Window{Ref: "w5", App: "TextEdit", Title: "Untitled", Role: "window", PID: 77})
		w.trees["w5"] = []protocol.Node{{Ref: "w5", Role: "window", RoleDesc: "standard window", Name: "Untitled"}}
	}
	c := session(t, w.engine())
	out := c.ok(`var te = await cua.getApp("com.apple.TextEdit")`)
	if !strings.HasPrefix(out, `Window: "Untitled", App: TextEdit.`) {
		t.Errorf("launched app state %q", out)
	}
	var launches, focuses int
	for _, s := range w.actSteps() {
		if s["launch"] != nil {
			launches++
			if s["launch"] != "TextEdit" {
				t.Errorf("launch %v", s)
			}
		}
		if s["focus"] != nil {
			focuses++
		}
	}
	if launches != 1 || focuses != 0 {
		t.Errorf("launches %d, focuses %d", launches, focuses)
	}
	r := c.js(`await cua.getApp("Nonexistent Thing")`)
	if !r.isError || !strings.Contains(r.all(), "bundle id from cua.listApps()") {
		t.Errorf("unknown app: %q", r.all())
	}
	// TODO(live): CDX-17 in the VM: background launch keeps the front app.
}

func quote(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// CDX-18: getState and listApps shapes; browsers is always empty.
func TestCDX18Inventory(t *testing.T) {
	c := session(t, newWorld().engine())
	out := c.ok(`await cua.getState()`)
	var st struct {
		Apps []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
			LastUsed    string `json:"lastUsedDate"`
			UseCount    int    `json:"useCount"`
			IsRunning   bool   `json:"isRunning"`
			Windows     []struct {
				ID    int    `json:"id"`
				App   string `json:"app"`
				Title string `json:"title"`
			} `json:"windows"`
		} `json:"apps"`
		Browsers []any `json:"browsers"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if st.Browsers == nil || len(st.Browsers) != 0 || len(st.Apps) != 2 {
		t.Fatalf("state %s", out)
	}
	a := st.Apps[0]
	if a.ID != "dev.runta.fixture" || a.DisplayName != "Runta CUA Fixture" || !a.IsRunning || len(a.Windows) != 1 || a.Windows[0].ID != 1 || a.Windows[0].Title != "Runta CUA Fixture" {
		t.Errorf("app %+v", a)
	}
	if b := st.Apps[1]; b.ID != "com.apple.TextEdit" || b.IsRunning || b.LastUsed != "2026-11-01" || b.UseCount != 3 {
		t.Errorf("app %+v", b)
	}
	if out := c.ok(`const l = await cua.listApps({emit: false}); nodeRepl.write(l.map(a => a.id).join(","))`); out != "dev.runta.fixture,com.apple.TextEdit" {
		t.Errorf("listApps %q", out)
	}
	if out := c.ok(`nodeRepl.write(cua.computer.target)`); out != "mac" {
		t.Errorf("target %q", out)
	}
}

// CDX-19: the browser APIs throw the documented error.
func TestCDX19Browsers(t *testing.T) {
	c := session(t, newWorld().engine())
	for _, fn := range []string{"getBrowser", "createBrowserTab", "getTab", "listBrowsers", "listTabs"} {
		r := c.js(`await cua.` + fn + `()`)
		if !r.isError || !strings.Contains(r.all(), `Error: browser surfaces are not enabled; use cua.getApp("<browser>")`) {
			t.Errorf("%s: %q", fn, r.all())
		}
	}
}

// CDX-20: an id the latest state does not hold, or an element that is
// gone, is an error asking for a fresh state.
func TestCDX20StaleIDs(t *testing.T) {
	w := newWorld()
	c := session(t, w.engine())
	c.ok(`var app = await cua.getApp("Runta CUA Fixture")`)
	r := c.js(`await app.click(99)`)
	if !r.isError || !strings.Contains(r.all(), "element 99 is not in the latest state") || !strings.Contains(r.all(), "getAXState()") {
		t.Errorf("unknown id: %q", r.all())
	}
	w.fail = func(map[string]any) *protocol.ActResult {
		zero := 0
		return &protocol.ActResult{OK: false, StoppedAt: &zero, Error: &protocol.Error{Code: protocol.ErrStaleRef, Message: "e2 is from an older view"},
			Steps: []protocol.StepResult{{OK: false, Status: protocol.StatusRefused}}}
	}
	r = c.js(`await app.click(2)`)
	if !r.isError || !strings.Contains(r.all(), "element 2 is no longer in the app; call getAXState() again") {
		t.Errorf("stale element: %q", r.all())
	}
}

// CDX-21: safety through cua calls: a refused step surfaces, Stop ends a
// call in flight, another agent's turn makes the call busy, and the session
// survives each.
func TestCDX21Safety(t *testing.T) {
	w := newWorld()
	e := w.engine()
	c := session(t, e)
	c.ok(`var app = await cua.getApp("Runta CUA Fixture")`)
	w.fail = func(map[string]any) *protocol.ActResult {
		zero := 0
		return &protocol.ActResult{OK: false, StoppedAt: &zero, Error: &protocol.Error{Code: protocol.ErrNotAllowed, Message: "Mail is not on the allowlist; nothing was sent"},
			Steps: []protocol.StepResult{{OK: false, Status: protocol.StatusRefused}}}
	}
	r := c.js(`await app.typeText("x")`)
	if !r.isError || !strings.Contains(r.all(), "not on the allowlist") {
		t.Errorf("refusal: %q", r.all())
	}
	w.fail = nil

	// Stop mid-call.
	done := make(chan result, 1)
	go func() { done <- c.js(`nodeRepl.write("started"); await new Promise(r => setTimeout(r, 5000))`) }()
	time.Sleep(150 * time.Millisecond)
	e.Stopper.Stop()
	select {
	case r := <-done:
		if !r.isError || !strings.Contains(r.all(), "stopped") || !strings.Contains(r.output(), "started") {
			t.Errorf("stop: %q", r.all())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not end the call")
	}
	e.Stopper.Resume()

	// Another agent holds the desk: the call is busy and sent nothing.
	release, err := e.Turn.Acquire(context.Background(), "other-agent")
	if err != nil {
		t.Fatal(err)
	}
	r = c.js(`await app.click(2)`)
	release()
	if !r.isError || !strings.Contains(r.all(), "busy") {
		t.Errorf("busy: %q", r.all())
	}
	if out := c.ok(`nodeRepl.write(typeof app.click)`); out != "function" {
		t.Errorf("session after safety stops: %q", out)
	}
	// TODO(live): CDX-21 in the VM: allowlist blackout, confirmation dialog
	// and wrong-window refusal through cua calls.
}

// CDX-22: the sandbox has no require, process, file system or network.
func TestCDX22Sandbox(t *testing.T) {
	c := session(t, newWorld().engine())
	out := c.ok(`nodeRepl.write([typeof require, typeof process, typeof fetch, typeof XMLHttpRequest, typeof Deno, typeof Bun, typeof module].join(","))`)
	if out != "undefined,undefined,undefined,undefined,undefined,undefined,undefined" {
		t.Errorf("globals %q", out)
	}
	r := c.js(`await import("fs")`)
	if !r.isError {
		t.Errorf("import: %q", r.all())
	}
}

// A call past its time limit ends with an error and the runtime stays.
func TestREPLTimeout(t *testing.T) {
	e := newWorld().engine()
	e.JSTimeout = 200 * time.Millisecond
	c := session(t, e)
	c.ok(`var kept = 7`)
	start := time.Now()
	r := c.js(`nodeRepl.write("before"); while (true) {}`)
	if !r.isError || !strings.Contains(r.all(), "limit") || !strings.Contains(r.output(), "before") {
		t.Errorf("timeout: %q", r.all())
	}
	if time.Since(start) > 3*time.Second {
		t.Error("the timeout did not interrupt the loop")
	}
	if out := c.ok(`nodeRepl.write(String(kept))`); out != "7" {
		t.Errorf("after timeout %q", out)
	}
}

// Text output is capped with a note saying how much was dropped.
func TestREPLOutputCap(t *testing.T) {
	c := session(t, newWorld().engine())
	out := c.ok(`nodeRepl.write("x".repeat(150000)); console.log("tail")`)
	if n := strings.Count(out, "x"); n != 100000 {
		t.Errorf("kept %d characters", n)
	}
	if !strings.Contains(out, "[output cut at 100000 characters: 50005 more were dropped") {
		t.Errorf("note: %q", out[len(out)-200:])
	}
}

// A password typed through fennec.act never comes back in the output.
func TestREPLSecretNotEchoed(t *testing.T) {
	d, act, _ := secretRig(t)
	_ = d
	e := newWorld().engine()
	e.Impl[protocol.ToolAct] = act
	c := session(t, e)
	out := c.ok(`const r = await fennec.act(null, [{type: "hunter2", secret: true, any_focus: true}, {key: "cmd+nonsense"}]); nodeRepl.write(JSON.stringify(r))`)
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "steps") {
		t.Errorf("output %q", out)
	}
}

// secretRig is a real act tool on a fake desk.
func secretRig(t *testing.T) (*fakedesk.Desk, engine.Tool, *policy.Policy) {
	t.Helper()
	d := fakedesk.New()
	d.Win("cg:10", "TextEdit", "Notes", 100, [4]int{0, 0, 800, 600}, true)
	d.Add("cg:10", "field", "textbox", "Password", fakedesk.Box(100, 100, 80, 20), "cg:10")
	p, err := policy.New(policy.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Activity.Idle = func() time.Duration { return time.Hour }
	x := &actions.Executor{B: d, Refs: view.NewRegistry(0), Policy: p, Sleep: func(context.Context, time.Duration) error { return nil }}
	return d, engine.ActTool(x, p, nil), p
}

// The CLI and harnesses reach an engine tool through js
// (mcpclient.EngineTool) and get the tool's own answer back.
func TestEngineToolThroughREPL(t *testing.T) {
	w := newWorld()
	e := w.engine()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		_ = mcpserver.Serve(context.Background(), inR, outW, e, mcpserver.Info{Name: "fennec-cua"})
		_ = outW.Close()
	}()
	t.Cleanup(func() { _ = inW.Close() })
	c := mcpclient.New(inW, outR)
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := c.EngineTool(ctx, "act", map[string]any{"window": "w1", "steps": []any{map[string]any{"key": "cmd+s"}}})
	if err != nil || r.IsError || !strings.Contains(r.Text(), `"ok":true`) || len(r.Content) != 1 {
		t.Fatalf("act: %+v %v", r, err)
	}
	if s := lastStep(w); s["key"] != "cmd+s" {
		t.Errorf("step %v", s)
	}
	r, _ = c.EngineTool(ctx, "observe", map[string]any{"bogus": 1})
	if !r.IsError || !strings.Contains(r.Text(), `"code":"invalid_argument"`) {
		t.Errorf("bad argument: %s", r.Text())
	}
	r, _ = c.EngineTool(ctx, "observe", map[string]any{"window": "w1", "image": "window"})
	if r.IsError || len(r.Content) != 2 || r.Content[1].Type != "image" {
		t.Errorf("capture: %+v", r.Content)
	}
	release, err := e.Turn.Acquire(ctx, "other-agent")
	if err != nil {
		t.Fatal(err)
	}
	r, _ = c.EngineTool(ctx, "act", map[string]any{"window": "w1", "steps": []any{map[string]any{"key": "cmd+s"}}})
	release()
	if !r.IsError || !strings.Contains(r.Text(), `"code":"busy"`) {
		t.Errorf("busy: %s", r.Text())
	}
	if r, _ := c.EngineTool(ctx, "status", nil); r.IsError || !strings.Contains(r.Text(), `"version"`) {
		t.Errorf("status: %s", r.Text())
	}
}
