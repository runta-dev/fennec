package browser

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/cdp"
	"fennec/cua/internal/protocol"
)

// A fake Chromium for unit tests: it speaks CDP over an in-memory pipe
// (NUL-framed JSON, as --remote-debugging-pipe does) and serves synthetic
// pages, recording every command and summarising input events.

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
	label    string // p1, p2, ... in creation order
	targetID string
	window   int
	opener   string
	url      string
	title    string
	root     *spec
	byB      map[int]*spec
	history  []string
	hindex   int
	loaders  int
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
	p.title = root.name
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

// fakeBrowser is the fake Chromium; it is also the manager's Launcher.
type fakeBrowser struct {
	mu         sync.Mutex
	pages      []*fakePage
	nextTarget int
	nextWindow int
	lastWindow int
	calls      []string // "p1 Method {params}" or "Method {params}"
	input      []string // "p1 down left 40 155 x1", "p1 key down Shift", ...
	cursor     []string // agent cursor updates
	cookies    []map[string]any
	// pages builds a page for a URL.
	build func(url string) *spec
	// delay holds a method's reply back.
	delay map[string]time.Duration
	// fail answers a method with an error.
	fail map[string]string
	// eval answers Runtime.callFunctionOn by the function's text.
	eval func(fn string, s *spec, args []any) (any, error)
	// shotWidth is the screenshot's pixel width.
	shotWidth int
	// starts counts browser starts.
	starts int
	conn   *cdp.Conn
	toPeer io.Closer // closes the fake's side
	wmu    sync.Mutex
	out    io.Writer
	off    bool // Available answers false
}

func newFake() *fakeBrowser {
	return &fakeBrowser{delay: map[string]time.Duration{}, fail: map[string]string{}, shotWidth: 800,
		build: func(url string) *spec { return n("RootWebArea", "Blank") }}
}

// Available is the Launcher's.
func (f *fakeBrowser) Available() bool { return !f.off }

// Conn is the Launcher's: a live connection, or a fresh browser start.
func (f *fakeBrowser) Conn(ctx context.Context) (*cdp.Conn, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn != nil {
		select {
		case <-f.conn.Done():
		default:
			return f.conn, f.starts, nil
		}
	}
	f.starts++
	f.pages = nil
	cmdR, cmdW := io.Pipe()
	repR, repW := io.Pipe()
	f.conn = cdp.New(repR, cmdW, func() { cmdW.Close(); repR.Close() })
	f.out = repW
	f.toPeer = closers{cmdR, repW}
	go f.serve(cmdR, repW)
	return f.conn, f.starts, nil
}

type closers []io.Closer

func (c closers) Close() error {
	for _, x := range c {
		x.Close()
	}
	return nil
}

// crash ends the browser as if its process died: every page is gone.
func (f *fakeBrowser) crash() {
	f.mu.Lock()
	c := f.toPeer
	f.pages = nil
	f.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

func (f *fakeBrowser) send(w io.Writer, m map[string]any) {
	data, _ := json.Marshal(m)
	f.wmu.Lock()
	_, _ = w.Write(append(data, 0))
	f.wmu.Unlock()
}

func (f *fakeBrowser) serve(r io.Reader, w io.Writer) {
	rd := bufio.NewReader(r)
	for {
		frame, err := rd.ReadBytes(0)
		if err != nil {
			return
		}
		var m struct {
			ID        int64           `json:"id"`
			SessionID string          `json:"sessionId"`
			Method    string          `json:"method"`
			Params    json.RawMessage `json:"params"`
		}
		if json.Unmarshal(frame[:len(frame)-1], &m) != nil {
			continue
		}
		go func() {
			f.mu.Lock()
			d := f.delay[m.Method]
			f.mu.Unlock()
			if d > 0 {
				time.Sleep(d)
			}
			v, events, err := f.handle(m.SessionID, m.Method, m.Params)
			out := map[string]any{"id": m.ID}
			if m.SessionID != "" {
				out["sessionId"] = m.SessionID
			}
			if err != nil {
				out["error"] = map[string]any{"code": -32000, "message": err.Error()}
			} else {
				out["result"] = v
			}
			f.send(w, out)
			for _, e := range events {
				f.send(w, e)
			}
		}()
	}
}

func (f *fakeBrowser) record(p *fakePage, method string, params json.RawMessage) {
	s := method
	if len(params) > 0 && string(params) != "{}" {
		s += " " + string(params)
	}
	if p != nil {
		s = p.label + " " + s
	}
	f.calls = append(f.calls, s)
}

// callsLike lists recorded calls that start with prefix.
func (f *fakeBrowser) callsLike(prefix string) []string {
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

// inputs lists summarised input events that start with prefix.
func (f *fakeBrowser) inputs(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.input {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeBrowser) clearCalls() {
	f.mu.Lock()
	f.calls, f.input, f.cursor = nil, nil, nil
	f.mu.Unlock()
}

func (f *fakeBrowser) page(sid string) *fakePage {
	for _, p := range f.pages {
		if "S"+p.targetID == sid {
			return p
		}
	}
	return nil
}

func (f *fakeBrowser) target(id string) *fakePage {
	for _, p := range f.pages {
		if p.targetID == id {
			return p
		}
	}
	return nil
}

// addPage opens a page the agent did not create: opener "" is a user tab
// in window w.
func (f *fakeBrowser) addPage(url string, w int, opener string) *fakePage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newPage(url, w, opener)
}

func (f *fakeBrowser) newPage(url string, w int, opener string) *fakePage {
	f.nextTarget++
	p := &fakePage{label: "p" + strconv.Itoa(f.nextTarget), targetID: fmt.Sprintf("T%03d", f.nextTarget), window: w, opener: opener, url: url}
	p.load(f.build(url))
	p.history = []string{url}
	f.pages = append(f.pages, p)
	return p
}

func pngOf(width int) string {
	png := make([]byte, 64)
	copy(png, "\x89PNG\r\n\x1a\n")
	binary.BigEndian.PutUint32(png[16:20], uint32(width))
	return base64.StdEncoding.EncodeToString(png)
}

// lifecycle answers the events of a new document in p.
func lifecycleEvents(p *fakePage) []map[string]any {
	p.loaders++
	loader := fmt.Sprintf("L%s-%d", p.targetID, p.loaders)
	var out []map[string]any
	for _, name := range []string{"init", "DOMContentLoaded", "load", "networkIdle"} {
		out = append(out, map[string]any{"method": "Page.lifecycleEvent", "sessionId": "S" + p.targetID,
			"params": map[string]any{"frameId": "F" + p.targetID, "loaderId": loader, "name": name}})
	}
	return out
}

func (f *fakeBrowser) handle(sid, method string, raw json.RawMessage) (any, []map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var params map[string]any
	_ = json.Unmarshal(raw, &params)
	str := func(k string) string { s, _ := params[k].(string); return s }
	var p *fakePage
	if sid != "" {
		p = f.page(sid)
		if p == nil {
			return nil, nil, fmt.Errorf("Session with given id not found.")
		}
	}
	f.record(p, method, raw)
	if msg, ok := f.fail[method]; ok {
		return nil, nil, fmt.Errorf("%s", msg)
	}
	if p == nil {
		return f.browserCall(method, params, str)
	}
	return f.pageCall(p, method, params, str)
}

func (f *fakeBrowser) browserCall(method string, params map[string]any, str func(string) string) (any, []map[string]any, error) {
	switch method {
	case "Browser.getVersion":
		return map[string]any{"product": "Chrome/160.0.8159.0"}, nil, nil
	case "Target.createTarget":
		w := f.lastWindow
		if nw, _ := params["newWindow"].(bool); nw || w == 0 {
			f.nextWindow++
			w = f.nextWindow
		}
		f.lastWindow = w
		p := f.newPage(str("url"), w, "")
		return map[string]any{"targetId": p.targetID}, nil, nil
	case "Target.attachToTarget":
		p := f.target(str("targetId"))
		if p == nil {
			return nil, nil, fmt.Errorf("No target with given id found")
		}
		return map[string]any{"sessionId": "S" + p.targetID}, nil, nil
	case "Target.detachFromTarget", "Target.activateTarget":
		return map[string]any{}, nil, nil
	case "Target.getTargets":
		var infos []any
		for _, p := range f.pages {
			infos = append(infos, map[string]any{"targetId": p.targetID, "type": "page", "title": p.title, "url": p.url, "openerId": p.opener})
		}
		infos = append(infos, map[string]any{"targetId": "SW1", "type": "service_worker", "url": "https://x.test/sw.js"})
		return map[string]any{"targetInfos": infos}, nil, nil
	case "Target.getTargetInfo":
		p := f.target(str("targetId"))
		if p == nil {
			return nil, nil, fmt.Errorf("No target with given id found")
		}
		return map[string]any{"targetInfo": map[string]any{"targetId": p.targetID, "type": "page", "title": p.title, "url": p.url}}, nil, nil
	case "Target.closeTarget":
		for i, p := range f.pages {
			if p.targetID == str("targetId") {
				f.pages = append(f.pages[:i], f.pages[i+1:]...)
				return map[string]any{"success": true}, nil, nil
			}
		}
		return nil, nil, fmt.Errorf("No target with given id found")
	case "Browser.getWindowForTarget":
		p := f.target(str("targetId"))
		if p == nil {
			return nil, nil, fmt.Errorf("No target with given id found")
		}
		return map[string]any{"windowId": p.window}, nil, nil
	case "Storage.setCookies":
		cs, _ := params["cookies"].([]any)
		for _, c := range cs {
			m := c.(map[string]any)
			if m["name"] == "reject" {
				return nil, nil, fmt.Errorf("Invalid cookie fields")
			}
		}
		for _, c := range cs {
			f.cookies = append(f.cookies, c.(map[string]any))
		}
		return map[string]any{}, nil, nil
	case "Storage.clearCookies":
		f.cookies = nil
		return map[string]any{}, nil, nil
	}
	return nil, nil, fmt.Errorf("'%s' wasn't found", method)
}

func (f *fakeBrowser) pageCall(p *fakePage, method string, params map[string]any, str func(string) string) (any, []map[string]any, error) {
	num := func(k string) float64 { v, _ := params[k].(float64); return v }
	backend := int(num("backendNodeId"))
	switch method {
	case "Page.enable", "Page.setLifecycleEventsEnabled":
		return map[string]any{}, nil, nil
	case "Page.navigate":
		u := str("url")
		if strings.Contains(u, "unreachable") {
			return map[string]any{"frameId": "F" + p.targetID, "errorText": "net::ERR_NAME_NOT_RESOLVED"}, nil, nil
		}
		p.url = u
		p.load(f.build(u))
		p.history = append(p.history[:p.hindex+1], u)
		p.hindex = len(p.history) - 1
		ev := lifecycleEvents(p)
		return map[string]any{"frameId": "F" + p.targetID, "loaderId": ev[0]["params"].(map[string]any)["loaderId"]}, ev, nil
	case "Page.reload":
		return map[string]any{}, lifecycleEvents(p), nil
	case "Page.getNavigationHistory":
		var entries []any
		for i, u := range p.history {
			entries = append(entries, map[string]any{"id": i + 100, "url": u})
		}
		return map[string]any{"currentIndex": p.hindex, "entries": entries}, nil, nil
	case "Page.navigateToHistoryEntry":
		id := int(num("entryId")) - 100
		p.hindex = id
		p.url = p.history[id]
		p.load(f.build(p.url))
		return map[string]any{}, lifecycleEvents(p), nil
	case "Page.captureScreenshot":
		return map[string]any{"data": pngOf(f.shotWidth)}, nil, nil
	case "Page.getLayoutMetrics":
		return map[string]any{"cssContentSize": map[string]any{"width": 1000, "height": 2400}}, nil, nil
	case "Input.dispatchMouseEvent":
		x, y := num("x"), num("y")
		switch str("type") {
		case "mouseMoved":
			f.input = append(f.input, fmt.Sprintf("%s move %g %g", p.label, x, y))
		case "mousePressed":
			f.input = append(f.input, fmt.Sprintf("%s down %s %g %g x%g mods%g", p.label, str("button"), x, y, num("clickCount"), num("modifiers")))
		case "mouseReleased":
			f.input = append(f.input, fmt.Sprintf("%s up %s %g %g x%g", p.label, str("button"), x, y, num("clickCount")))
		case "mouseWheel":
			f.input = append(f.input, fmt.Sprintf("%s wheel %g %g", p.label, num("deltaX"), num("deltaY")))
		}
		return map[string]any{}, nil, nil
	case "Input.dispatchKeyEvent":
		dir := "down"
		if str("type") == "keyUp" {
			dir = "up"
		}
		s := fmt.Sprintf("%s key %s %s", p.label, dir, str("key"))
		if t := str("text"); t != "" {
			s += " text=" + strconv.Quote(t)
		}
		if c, ok := params["commands"].([]any); ok {
			s += fmt.Sprintf(" %v", c)
		}
		f.input = append(f.input, s)
		return map[string]any{}, nil, nil
	case "Input.insertText":
		f.input = append(f.input, p.label+" insert "+str("text"))
		return map[string]any{}, nil, nil
	case "Accessibility.getFullAXTree":
		return axJSON(p.root, p.url), nil, nil
	case "DOM.getDocument":
		return domJSON(p.root), nil, nil
	case "DOM.scrollIntoViewIfNeeded", "DOM.focus":
		if p.byB[backend] == nil {
			return nil, nil, fmt.Errorf("No node with given id found")
		}
		return map[string]any{}, nil, nil
	case "DOM.getContentQuads":
		s := p.byB[backend]
		if s == nil || s.box[2] == 0 {
			return map[string]any{"quads": []any{}}, nil, nil
		}
		x, y, w, h := s.box[0], s.box[1], s.box[2], s.box[3]
		return map[string]any{"quads": [][]float64{{x, y, x + w, y, x + w, y + h, x, y + h}}}, nil, nil
	case "DOM.getBoxModel":
		return nil, nil, fmt.Errorf("Could not compute box model.")
	case "DOM.resolveNode":
		if p.byB[backend] == nil {
			return nil, nil, fmt.Errorf("No node with given id found")
		}
		return map[string]any{"object": map[string]any{"objectId": "obj-" + strconv.Itoa(backend)}}, nil, nil
	case "DOM.querySelectorAll":
		sel := str("selector")
		var ids []int
		for _, s := range p.ordered() {
			if (strings.HasPrefix(sel, "#") && s.domID == sel[1:]) || (s.tag != "" && s.tag == sel) {
				ids = append(ids, s.backend*10)
			}
		}
		return map[string]any{"nodeIds": ids}, nil, nil
	case "Runtime.callFunctionOn":
		b, _ := strconv.Atoi(strings.TrimPrefix(str("objectId"), "obj-"))
		fn := str("functionDeclaration")
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
			return exception("Error: " + err.Error()), nil, nil
		}
		return map[string]any{"result": map[string]any{"type": "object", "value": v}}, nil, nil
	case "Runtime.evaluate":
		return f.evaluate(p, str("expression")), nil, nil
	}
	return nil, nil, fmt.Errorf("'%s' wasn't found", method)
}

func exception(desc string) map[string]any {
	return map[string]any{"result": map[string]any{"type": "object"}, "exceptionDetails": map[string]any{"text": "Uncaught", "exception": map[string]any{"description": desc + "\n    at <anonymous>:1:7"}}}
}

func value(v any) map[string]any {
	return map[string]any{"result": map[string]any{"type": "object", "value": v}}
}

// spreadArgs reads the arguments of "(js)(...[a, b])".
func spreadArgs(expr string) []any {
	i := strings.LastIndex(expr, "(...")
	if i < 0 {
		return nil
	}
	var args []any
	_ = json.Unmarshal([]byte(strings.TrimSuffix(expr[i+4:], ")")), &args)
	return args
}

func (f *fakeBrowser) evaluate(p *fakePage, expr string) map[string]any {
	switch {
	case strings.Contains(expr, "fennec-agent-cursor"):
		a := spreadArgs(expr)
		f.cursor = append(f.cursor, fmt.Sprintf("%s %v", p.label, a))
		return value(true)
	case strings.Contains(expr, "devicePixelRatio"):
		return value(map[string]any{"w": 1000, "h": 600, "dpr": 1, "x": 0, "y": 0})
	case expr == "document.readyState":
		return value("complete")
	case strings.Contains(expr, "ClipboardEvent"):
		a := spreadArgs(expr)
		data, _ := json.Marshal(a)
		f.input = append(f.input, p.label+" paste "+string(data))
		return value("insert")
	case strings.Contains(expr, "activeElement"):
		return value(false)
	case strings.Contains(expr, "throw"):
		return exception("Error: boom")
	}
	return value(42)
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

// gateRec is a recording confirm gate.
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

func newTestManager(t *testing.T, f *fakeBrowser) (*Manager, *gateRec) {
	t.Helper()
	g := &gateRec{}
	m := NewManager(Options{Browser: f, Gate: g, Timeout: 2 * time.Second})
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
