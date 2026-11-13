package ego

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/protocol"
)

// A fake ego lite for unit tests: an in-process "bridge" that dials the
// client's socket like bridge.js does and serves its ops against synthetic
// pages, recording every call.

// spec describes one node of a synthetic page; build turns it into the
// CDP accessibility tree and document Chromium would report.
type spec struct {
	role     string
	name     string
	nameFrom string // contents (default), attribute, relatedElement, placeholder
	value    string
	props    map[string]any
	domID    string
	tag      string // element name; "#text" for a text node
	attrs    map[string]string
	password bool
	ignored  bool
	kids     []*spec
	backend  int
	box      [4]float64 // x, y, w, h
}

func n(role, name string, kids ...*spec) *spec {
	return &spec{role: role, name: name, kids: kids}
}

func (s *spec) with(k string, v any) *spec {
	if s.props == nil {
		s.props = map[string]any{}
	}
	s.props[k] = v
	return s
}

func (s *spec) id(id string) *spec          { s.domID = id; return s }
func (s *spec) from(f string) *spec         { s.nameFrom = f; return s }
func (s *spec) val(v string) *spec          { s.value = v; return s }
func (s *spec) at(x, y, w, h float64) *spec { s.box = [4]float64{x, y, w, h}; return s }
func (s *spec) attr(k, v string) *spec {
	if s.attrs == nil {
		s.attrs = map[string]string{}
	}
	s.attrs[k] = v
	return s
}

// fakePage is one tab of the fake.
type fakePage struct {
	label    string
	targetID string
	url      string
	title    string
	openedBy string
	root     *spec
	byB      map[int]*spec
	history  []string
	hindex   int
}

func (p *fakePage) load(root *spec) {
	p.root = root
	p.byB = map[int]*spec{}
	next := 2
	var number func(s *spec)
	number = func(s *spec) {
		s.backend = next
		next++
		p.byB[s.backend] = s
		for _, k := range s.kids {
			number(k)
		}
	}
	number(root)
	if root.name != "" {
		p.title = root.name
	}
}

func axJSON(root *spec, url string) json.RawMessage {
	var nodes []map[string]any
	var walk func(s *spec, parent int)
	walk = func(s *spec, parent int) {
		node := map[string]any{"nodeId": strconv.Itoa(s.backend), "ignored": s.ignored, "role": map[string]any{"type": "role", "value": s.role}, "backendDOMNodeId": s.backend}
		if s.name != "" {
			from := s.nameFrom
			if from == "" {
				from = "contents"
			}
			src := map[string]any{"type": from, "value": map[string]any{"type": "computedString", "value": s.name}}
			if from == "attribute" {
				src["attribute"] = "aria-label"
			}
			node["name"] = map[string]any{"type": "computedString", "value": s.name, "sources": []any{src}}
		}
		if s.value != "" {
			node["value"] = map[string]any{"type": "string", "value": s.value}
		}
		props := []any{}
		for k, v := range s.props {
			props = append(props, map[string]any{"name": k, "value": map[string]any{"type": "booleanOrUndefined", "value": v}})
		}
		if s.role == "RootWebArea" {
			props = append(props, map[string]any{"name": "url", "value": map[string]any{"type": "string", "value": url}})
		}
		node["properties"] = props
		if parent != 0 {
			node["parentId"] = strconv.Itoa(parent)
		}
		var kids []string
		for _, k := range s.kids {
			kids = append(kids, strconv.Itoa(k.backend))
		}
		node["childIds"] = kids
		nodes = append(nodes, node)
		for _, k := range s.kids {
			walk(k, s.backend)
		}
	}
	walk(root, 0)
	data, _ := json.Marshal(map[string]any{"nodes": nodes})
	return data
}

func domJSON(root *spec) json.RawMessage {
	var build func(s *spec) map[string]any
	build = func(s *spec) map[string]any {
		if s.role == "StaticText" {
			return map[string]any{"nodeId": s.backend * 10, "backendNodeId": s.backend, "nodeType": 3, "localName": "", "nodeValue": s.name}
		}
		tag := s.tag
		if tag == "" {
			tag = "div"
		}
		var attrs []string
		if s.domID != "" {
			attrs = append(attrs, "id", s.domID)
		}
		for k, v := range s.attrs {
			attrs = append(attrs, k, v)
		}
		out := map[string]any{"nodeId": s.backend * 10, "backendNodeId": s.backend, "nodeType": 1, "localName": tag, "attributes": attrs}
		var kids []any
		for _, k := range s.kids {
			kids = append(kids, build(k))
		}
		out["children"] = kids
		return out
	}
	doc := map[string]any{"nodeId": 1, "backendNodeId": 1, "nodeType": 9, "localName": "", "children": []any{build(root)}}
	data, _ := json.Marshal(map[string]any{"root": doc})
	return data
}

// fakeEgo is the browser behind the fake bridges.
type fakeEgo struct {
	mu        sync.Mutex
	spaces    map[int]*fakeSpace
	nextSpace int
	nextPage  int
	userNames map[string]bool // names held by user-owned spaces
	calls     []string
	// pages builds a page for a URL.
	pages func(url string) *spec
	// delay holds a method's reply back.
	delay map[string]time.Duration
	// fail answers a method with an error.
	fail map[string]string
	// eval answers Runtime.callFunctionOn by the function's text.
	eval func(fn string, s *spec, args []any) (any, error)
	// shotWidth is the screenshot's pixel width.
	shotWidth int
	// starts counts bridges started; conns are the live ones.
	starts int
	conns  []net.Conn
	// badToken makes a bridge present the wrong token first.
	badToken bool
}

type fakeSpace struct {
	id        int
	name      string
	ownership string
	pages     []*fakePage
	finished  []string // kept labels at finish
	done      bool
}

func newFake() *fakeEgo {
	return &fakeEgo{spaces: map[int]*fakeSpace{}, userNames: map[string]bool{}, delay: map[string]time.Duration{}, fail: map[string]string{}, shotWidth: 800,
		pages: func(url string) *spec { return n("RootWebArea", "Blank") }}
}

func (f *fakeEgo) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

// callsLike lists recorded calls that start with prefix.
func (f *fakeEgo) callsLike(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeEgo) clearCalls() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

type fakeProc struct {
	done chan struct{}
	once sync.Once
	conn net.Conn
}

func (p *fakeProc) Kill() {
	p.once.Do(func() {
		if p.conn != nil {
			p.conn.Close()
		}
		close(p.done)
	})
}

func (p *fakeProc) Done() <-chan struct{} { return p.done }

// starter is the fake's Starter.
func (f *fakeEgo) starter() Starter {
	return func(ctx context.Context, sock, token, script string) (Proc, error) {
		if !strings.Contains(script, "const TOKEN = \""+token+"\"") || !strings.Contains(script, "const SOCK = ") {
			return nil, fmt.Errorf("the script does not define SOCK and TOKEN")
		}
		f.mu.Lock()
		f.starts++
		bad := f.badToken
		f.mu.Unlock()
		p := &fakeProc{done: make(chan struct{})}
		go func() {
			if bad {
				if c, err := net.Dial("unix", sock); err == nil {
					fmt.Fprintln(c, `{"hello":"not-the-token"}`)
					c.Close()
				}
			}
			conn, err := net.Dial("unix", sock)
			if err != nil {
				p.Kill()
				return
			}
			p.conn = conn
			f.mu.Lock()
			f.conns = append(f.conns, conn)
			f.mu.Unlock()
			fmt.Fprintf(conn, "{\"hello\":%q}\n", token)
			f.serve(conn, p)
		}()
		return p, nil
	}
}

// crash ends every live bridge as if its process died.
func (f *fakeEgo) crash() {
	f.mu.Lock()
	conns := f.conns
	f.conns = nil
	f.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (f *fakeEgo) serve(conn net.Conn, p *fakeProc) {
	defer p.Kill()
	rd := bufio.NewReader(conn)
	var wmu sync.Mutex
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID   int64           `json:"id"`
			Op   string          `json:"op"`
			Args json.RawMessage `json:"args"`
		}
		if json.Unmarshal(line, &req) != nil {
			continue
		}
		if req.Op == "bye" {
			conn.Close()
			return
		}
		go func() {
			v, err := f.handle(req.Op, req.Args)
			out := map[string]any{"id": req.ID, "ok": err == nil, "value": v}
			if err != nil {
				code := ""
				if re, ok := err.(*RemoteError); ok {
					code = re.Code
				}
				out["error"] = map[string]any{"message": err.Error(), "code": code}
			}
			data, _ := json.Marshal(out)
			wmu.Lock()
			conn.Write(append(data, '\n'))
			wmu.Unlock()
		}()
	}
}

func (f *fakeEgo) space(id int) (*fakeSpace, error) {
	sp := f.spaces[id]
	if sp == nil || sp.done {
		return nil, fmt.Errorf("space %d is not open", id)
	}
	return sp, nil
}

func (f *fakeEgo) newPage(sp *fakeSpace, url string) *fakePage {
	f.nextPage++
	p := &fakePage{label: "p" + strconv.Itoa(len(sp.pages)+1), targetID: fmt.Sprintf("T%03d", f.nextPage), openedBy: "agent"}
	p.url = url
	p.load(f.pages(url))
	p.history = []string{url}
	sp.pages = append(sp.pages, p)
	return p
}

func (f *fakeEgo) handle(op string, raw json.RawMessage) (any, error) {
	var a struct {
		Name     string            `json:"name"`
		SpaceID  int               `json:"spaceId"`
		TargetID string            `json:"targetId"`
		Page     string            `json:"page"`
		Method   string            `json:"method"`
		Args     []json.RawMessage `json:"args"`
	}
	_ = json.Unmarshal(raw, &a)
	key := a.Method
	if key == "" {
		key = op
	}
	f.mu.Lock()
	d := f.delay[key]
	msg, failing := f.fail[key]
	f.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	args := make([]string, len(a.Args))
	for i, x := range a.Args {
		args[i] = string(x)
	}
	f.calls = append(f.calls, strings.TrimSpace(fmt.Sprintf("%s %s %s %s", op, a.Page, a.Method, strings.Join(args, " "))))
	if failing {
		return nil, &RemoteError{Message: msg}
	}
	switch op {
	case "spaces":
		var out []any
		for _, sp := range f.spaces {
			out = append(out, map[string]any{"id": sp.id, "name": sp.name, "ownership": sp.ownership})
		}
		return out, nil
	case "space":
		if f.userNames[a.Name] {
			return nil, &RemoteError{Message: "EGO_TASK_SPACE_NAME_IN_USE: the name belongs to a user space", Code: "EGO_TASK_SPACE_NAME_IN_USE"}
		}
		for _, sp := range f.spaces {
			if sp.name == a.Name && !sp.done {
				return map[string]any{"spaceId": sp.id, "name": sp.name, "ownership": sp.ownership}, nil
			}
		}
		f.nextSpace++
		sp := &fakeSpace{id: f.nextSpace, name: a.Name, ownership: "agent"}
		f.spaces[sp.id] = sp
		f.newPage(sp, "about:blank")
		return map[string]any{"spaceId": sp.id, "name": sp.name, "ownership": sp.ownership}, nil
	case "resume", "takeover":
		sp, err := f.space(a.SpaceID)
		if err != nil {
			return nil, err
		}
		if op == "takeover" {
			sp.ownership = "agent"
		} else if sp.ownership != "agent" {
			return nil, fmt.Errorf("the space is the user's")
		}
		return map[string]any{"spaceId": sp.id, "name": sp.name, "ownership": sp.ownership}, nil
	case "adopt":
		sp, err := f.space(a.SpaceID)
		if err != nil {
			return nil, err
		}
		for _, p := range sp.pages {
			if p.targetID == a.TargetID {
				if p.label == "" {
					p.label = "p" + strconv.Itoa(len(sp.pages)+10)
				}
				return map[string]any{"label": p.label}, nil
			}
		}
		return nil, fmt.Errorf("the tab is gone")
	case "call":
		sp, err := f.space(a.SpaceID)
		if err != nil {
			return nil, err
		}
		if sp.ownership != "agent" {
			return nil, fmt.Errorf("the user controls this space")
		}
		if a.Page == "" {
			return f.taskCall(sp, a.Method, a.Args)
		}
		for _, p := range sp.pages {
			if p.label == a.Page {
				return f.pageCall(sp, p, a.Method, a.Args)
			}
		}
		return nil, fmt.Errorf("no page %s", a.Page)
	}
	return nil, fmt.Errorf("unknown op %s", op)
}

func (f *fakeEgo) taskCall(sp *fakeSpace, method string, args []json.RawMessage) (any, error) {
	switch method {
	case "tabs":
		var out []any
		for _, p := range sp.pages {
			out = append(out, map[string]any{"label": p.label, "targetId": p.targetID, "title": p.title, "url": p.url, "openedBy": p.openedBy})
		}
		return out, nil
	case "newPage":
		p := f.newPage(sp, "about:blank")
		return map[string]any{"label": p.label, "targetId": p.targetID}, nil
	case "handOff":
		sp.ownership = "agentDelegatedToUser"
		return nil, nil
	case "finish":
		var o struct {
			Keep []string `json:"keep"`
		}
		_ = json.Unmarshal(args[0], &o)
		sp.finished = o.Keep
		sp.done = true
		return map[string]any{"closedSpace": len(o.Keep) == 0}, nil
	}
	return nil, fmt.Errorf("no task method %s", method)
}

func (f *fakeEgo) pageCall(sp *fakeSpace, p *fakePage, method string, args []json.RawMessage) (any, error) {
	str := func(i int) string {
		var s string
		if i < len(args) {
			_ = json.Unmarshal(args[i], &s)
		}
		return s
	}
	switch method {
	case "goto":
		u := str(0)
		p.url = u
		p.load(f.pages(u))
		p.history = append(p.history[:p.hindex+1], u)
		p.hindex = len(p.history) - 1
		return nil, nil
	case "reload", "waitForLoadState", "waitForURL":
		return nil, nil
	case "info":
		return map[string]any{"url": p.url, "title": p.title, "w": 1000, "h": 600}, nil
	case "close":
		for i, x := range sp.pages {
			if x == p {
				sp.pages = append(sp.pages[:i], sp.pages[i+1:]...)
				break
			}
		}
		return nil, nil
	case "screenshot":
		var o struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(args[0], &o)
		png := make([]byte, 64)
		copy(png, "\x89PNG\r\n\x1a\n")
		binary.BigEndian.PutUint32(png[16:20], uint32(f.shotWidth))
		if err := os.WriteFile(o.Path, png, 0o600); err != nil {
			return nil, err
		}
		return o.Path, nil
	case "cdp":
		return f.cdp(p, str(0), args)
	}
	if strings.HasPrefix(method, "mouse.") || strings.HasPrefix(method, "keyboard.") {
		return nil, nil
	}
	return nil, fmt.Errorf("no page method %s", method)
}

func (f *fakeEgo) cdp(p *fakePage, method string, args []json.RawMessage) (any, error) {
	var params map[string]any
	if len(args) > 1 {
		_ = json.Unmarshal(args[1], &params)
	}
	backend := 0
	if b, ok := params["backendNodeId"].(float64); ok {
		backend = int(b)
	}
	switch method {
	case "Accessibility.getFullAXTree":
		return axJSON(p.root, p.url), nil
	case "DOM.getDocument":
		return domJSON(p.root), nil
	case "DOM.scrollIntoViewIfNeeded", "DOM.focus":
		if p.byB[backend] == nil {
			return nil, fmt.Errorf("no node %d", backend)
		}
		return map[string]any{}, nil
	case "DOM.getContentQuads":
		s := p.byB[backend]
		if s == nil || s.box[2] == 0 {
			return map[string]any{"quads": []any{}}, nil
		}
		x, y, w, h := s.box[0], s.box[1], s.box[2], s.box[3]
		return map[string]any{"quads": [][]float64{{x, y, x + w, y, x + w, y + h, x, y + h}}}, nil
	case "DOM.getBoxModel":
		return nil, fmt.Errorf("no box")
	case "DOM.resolveNode":
		if p.byB[backend] == nil {
			return nil, fmt.Errorf("no node")
		}
		return map[string]any{"object": map[string]any{"objectId": "obj-" + strconv.Itoa(backend)}}, nil
	case "DOM.querySelectorAll":
		sel, _ := params["selector"].(string)
		var ids []int
		for _, s := range p.ordered() {
			if (strings.HasPrefix(sel, "#") && s.domID == sel[1:]) || (s.tag != "" && s.tag == sel) {
				ids = append(ids, s.backend*10)
			}
		}
		return map[string]any{"nodeIds": ids}, nil
	case "Runtime.callFunctionOn":
		obj, _ := params["objectId"].(string)
		b, _ := strconv.Atoi(strings.TrimPrefix(obj, "obj-"))
		fn, _ := params["functionDeclaration"].(string)
		var vals []any
		if as, ok := params["arguments"].([]any); ok {
			for _, x := range as {
				vals = append(vals, x.(map[string]any)["value"])
			}
		}
		var v any
		var err error
		if f.eval != nil {
			v, err = f.eval(fn, p.byB[b], vals)
		} else {
			v, err = defaultEval(fn, p.byB[b], vals)
		}
		if err != nil {
			return map[string]any{"result": map[string]any{"type": "object"}, "exceptionDetails": map[string]any{"text": "Uncaught", "exception": map[string]any{"description": "Error: " + err.Error() + "\n    at <anonymous>"}}}, nil
		}
		return map[string]any{"result": map[string]any{"type": "object", "value": v}}, nil
	case "Runtime.evaluate":
		expr, _ := params["expression"].(string)
		if strings.Contains(expr, "activeElement") {
			return map[string]any{"result": map[string]any{"type": "boolean", "value": false}}, nil
		}
		if strings.Contains(expr, "throw") {
			return map[string]any{"result": map[string]any{"type": "object"}, "exceptionDetails": map[string]any{"text": "Uncaught", "exception": map[string]any{"description": "Error: boom\n    at <anonymous>:1:7"}}}, nil
		}
		return map[string]any{"result": map[string]any{"type": "number", "value": 42}}, nil
	case "Page.getNavigationHistory":
		var entries []any
		for i, u := range p.history {
			entries = append(entries, map[string]any{"id": i + 100, "url": u})
		}
		return map[string]any{"currentIndex": p.hindex, "entries": entries}, nil
	case "Page.navigateToHistoryEntry":
		id := int(params["entryId"].(float64)) - 100
		p.hindex = id
		p.url = p.history[id]
		p.load(f.pages(p.url))
		return map[string]any{}, nil
	}
	return nil, fmt.Errorf("unsupported CDP %s", method)
}

func (p *fakePage) ordered() []*spec {
	var out []*spec
	var walk func(s *spec)
	walk = func(s *spec) {
		out = append(out, s)
		for _, k := range s.kids {
			walk(k)
		}
	}
	walk(p.root)
	return out
}

// defaultEval answers the element functions the package sends.
func defaultEval(fn string, s *spec, args []any) (any, error) {
	if s == nil {
		return nil, fmt.Errorf("no element")
	}
	switch {
	case strings.Contains(fn, "el.type === 'password'"):
		return s.password, nil
	case strings.Contains(fn, "return ('value' in el"):
		if s.value != "" {
			return s.value, nil
		}
		return s.name, nil
	case strings.Contains(fn, "setSelectionRange"):
		return true, nil
	case strings.Contains(fn, "getOwnPropertyDescriptor"):
		if s.role == "button" {
			return nil, fmt.Errorf("this element takes no value; click it, or focus it and use typeText (value %v)", args)
		}
		s.value = fmt.Sprint(args[0])
		return true, nil
	case strings.Contains(fn, "aria-checked"):
		return s.props["checked"] == true || s.props["checked"] == "true", nil
	case strings.Contains(fn, "return el.innerText"):
		return s.name, nil
	}
	return nil, nil
}

// testManager is a manager over a fake with a recording gate.
type gateRec struct {
	mu    sync.Mutex
	asked []string
	deny  bool
}

func (g *gateRec) Ask(ctx context.Context, steps []protocol.Step, target string) *protocol.Error {
	g.mu.Lock()
	defer g.mu.Unlock()
	data, _ := json.Marshal(steps)
	g.asked = append(g.asked, string(data)+" @ "+target)
	if g.deny {
		return &protocol.Error{Code: protocol.ErrDeclined, Message: "the user declined; nothing was sent"}
	}
	return nil
}

func newTestManager(t *testing.T, f *fakeEgo) (*Manager, *gateRec) {
	t.Helper()
	dir, err := os.MkdirTemp("", "fe")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	g := &gateRec{}
	m := NewManager(Options{Dir: dir, Start: f.starter(), Gate: g, Timeout: 2 * time.Second})
	t.Cleanup(func() { m.CloseAll(context.Background()) })
	return m, g
}

// call runs one browser call and returns its JSON result or error.
func call(t *testing.T, m *Manager, key any, req map[string]any) (map[string]any, *protocol.Error) {
	t.Helper()
	return callCtx(context.Background(), t, m, key, req)
}

func callCtx(ctx context.Context, t *testing.T, m *Manager, key any, req map[string]any) (map[string]any, *protocol.Error) {
	t.Helper()
	data, _ := json.Marshal(req)
	v, perr := m.Call(ctx, key, data)
	if perr != nil {
		return nil, perr
	}
	out, _ := json.Marshal(v)
	res := map[string]any{}
	if json.Unmarshal(out, &res) != nil {
		var plain any
		_ = json.Unmarshal(out, &plain)
		res = map[string]any{"value": plain}
	}
	return res, nil
}

func mustCall(t *testing.T, m *Manager, key any, req map[string]any) map[string]any {
	t.Helper()
	res, perr := call(t, m, key, req)
	if perr != nil {
		t.Fatalf("%v: %s: %s", req, perr.Code, perr.Message)
	}
	return res
}
