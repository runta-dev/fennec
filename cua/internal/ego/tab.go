package ego

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"fennec/cua/internal/protocol"
)

// Tab methods: the reference's Tab (Target<Uint8Array> plus the
// element-index-first input methods, navigation and marks).

const indexError = "Browser input requires an element index from the latest AX snapshot, or null to use current focus."

func (s *Session) bindingOf(req request) (*binding, *protocol.Error) {
	b := s.bindings[req.Binding]
	if b == nil {
		return nil, invalid("this tab binding is from a runtime that was reset; get the tab again with cua.getTab()")
	}
	if b.tab.closed {
		return nil, failed("Tab " + b.tab.id + " was closed; get or create a fresh tab from this browser.")
	}
	return b, nil
}

func arg(req request, i int) json.RawMessage {
	if i < len(req.Args) {
		return req.Args[i]
	}
	return nil
}

func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// tabOptions holds every option a Tab method takes.
type tabOptions struct {
	Emit           *bool    `json:"emit"`
	DisableDiffing bool     `json:"disableDiffing"`
	MouseButton    string   `json:"mouseButton"`
	ClickCount     *int     `json:"clickCount"`
	Key            string   `json:"key"`
	DurationMs     *float64 `json:"durationMs"`
	Prefix         string   `json:"prefix"`
	Suffix         string   `json:"suffix"`
	SelectionType  string   `json:"selectionType"`
	Format         string   `json:"format"`
	Pixels         *float64 `json:"pixels"`
	FullPage       bool     `json:"fullPage"`
	State          string   `json:"state"`
	TimeoutMs      *float64 `json:"timeoutMs"`
	Timeout        *float64 `json:"timeout"`
}

func options(raw json.RawMessage) (tabOptions, *protocol.Error) {
	var o tabOptions
	if isNull(raw) {
		return o, nil
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return o, invalid("bad options: " + err.Error())
	}
	return o, nil
}

func duration(o tabOptions) (time.Duration, bool, *protocol.Error) {
	if o.DurationMs == nil {
		return 0, false, nil
	}
	d := *o.DurationMs
	if d != math.Trunc(d) || d < 0 || d > 30000 {
		return 0, false, invalid("durationMs must be an integer from 0 to 30000 milliseconds.")
	}
	return time.Duration(d) * time.Millisecond, true, nil
}

// elementIndex reads an element index; null is allowed when nullable.
func elementIndex(raw json.RawMessage, nullable bool) (int, bool, *protocol.Error) {
	if isNull(raw) {
		if nullable {
			return 0, false, nil
		}
		return 0, false, invalid("an element id is a whole number from the latest getAXState()")
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || f != math.Trunc(f) || f < 0 {
		if nullable {
			return 0, false, invalid(indexError)
		}
		return 0, false, invalid("an element id is a whole number from the latest getAXState()")
	}
	return int(f), true, nil
}

func (s *Session) element(b *binding, id int) (Element, *protocol.Error) {
	e, ok := b.mem.Element(id)
	if !ok {
		return Element{}, failed(fmt.Sprintf("element %d is not in the latest state of tab %s; call getAXState() again and use an id from it", id, b.tab.id))
	}
	return e, nil
}

func (s *Session) tabCall(ctx context.Context, req request) (any, *protocol.Error) {
	b, perr := s.bindingOf(req)
	if perr != nil {
		return nil, perr
	}
	t := b.tab
	switch req.Method {
	case "getAXState":
		o, perr := options(arg(req, 0))
		if perr != nil {
			return nil, perr
		}
		text, perr := s.readState(ctx, b, o.DisableDiffing, true)
		if perr != nil {
			return nil, perr
		}
		return map[string]any{"text": text}, nil
	case "getScreenshot", "screenshot":
		o, perr := options(arg(req, 0))
		if perr != nil {
			return nil, perr
		}
		data, perr := s.shoot(ctx, t, o.FullPage)
		if perr != nil {
			return nil, perr
		}
		return map[string]any{"data": base64.StdEncoding.EncodeToString(data), "mime": "image/png"}, nil
	case "getAXStateAndScreenshot":
		o, perr := options(arg(req, 0))
		if perr != nil {
			return nil, perr
		}
		text, perr := s.readState(ctx, b, o.DisableDiffing, true)
		if perr != nil {
			return nil, perr
		}
		out := map[string]any{"text": text}
		if data, perr := s.shoot(ctx, t, false); perr == nil {
			out["data"], out["mime"] = base64.StdEncoding.EncodeToString(data), "image/png"
		}
		return out, nil
	case "domSnapshot":
		tree, _, perr := s.trees(ctx, t, false)
		if perr != nil {
			return nil, perr
		}
		return map[string]any{"text": AriaSnapshot(tree)}, nil
	case "title", "url":
		if _, err := s.pageInfo(ctx, t); err != nil {
			return nil, s.fail(err, "")
		}
		if req.Method == "title" {
			return t.title, nil
		}
		return t.url, nil
	case "click":
		return s.click(ctx, b, arg(req, 0), arg(req, 1))
	case "drag":
		return s.drag(ctx, b, arg(req, 0), arg(req, 1))
	case "scroll":
		return s.scroll(ctx, b, arg(req, 0), arg(req, 1), arg(req, 2))
	case "selectText":
		return s.selectText(ctx, b, arg(req, 0), arg(req, 1), arg(req, 2))
	case "setValue":
		return s.setValue(ctx, b, arg(req, 0), arg(req, 1))
	case "performSecondaryAction":
		return s.perform(ctx, b, arg(req, 0), arg(req, 1))
	case "typeText":
		return s.typeText(ctx, b, arg(req, 0), arg(req, 1))
	case "pressKey":
		return s.pressKey(ctx, b, arg(req, 0), arg(req, 1), arg(req, 2))
	case "paste":
		return s.paste(ctx, b, arg(req, 0), arg(req, 1), arg(req, 2))
	case "goto":
		var u string
		if json.Unmarshal(arg(req, 0), &u) != nil || u == "" {
			return nil, invalid("goto takes a URL")
		}
		if perr := s.ask(ctx, protocol.Step{Launch: u}, t.title); perr != nil {
			return nil, perr
		}
		t.dirty = true
		if _, err := s.page(ctx, t, "goto", u); err != nil {
			return nil, s.fail(err, "")
		}
		return nil, nil
	case "reload":
		t.dirty = true
		if _, err := s.page(ctx, t, "reload"); err != nil {
			return nil, s.fail(err, "")
		}
		return nil, nil
	case "back", "forward":
		return s.history(ctx, t, req.Method == "back")
	case "close":
		if !t.agent {
			return nil, &protocol.Error{Code: protocol.ErrNotAllowed, Message: "Tab " + t.id + " was opened by the user; it is left open"}
		}
		if _, err := s.page(ctx, t, "close"); err != nil {
			return nil, s.fail(err, "")
		}
		s.forget(t)
		return nil, nil
	case "markDeliverable":
		t.mark = "deliverable"
		return nil, nil
	case "markHandoff":
		t.mark = "handoff"
		if _, err := s.task(ctx, "handOff"); err != nil {
			return nil, s.fail(err, "")
		}
		s.handedOff = true
		return nil, nil
	case "waitForLoadState":
		var state string
		_ = json.Unmarshal(arg(req, 0), &state)
		o, _ := options(arg(req, 1))
		if state == "" {
			var so tabOptions
			if json.Unmarshal(arg(req, 0), &so) == nil {
				state, o = so.State, so
			}
		}
		if state == "" {
			state = "load"
		}
		if _, err := s.page(ctx, t, "waitForLoadState", state, waitOpts(o)); err != nil {
			return nil, s.fail(err, "")
		}
		return nil, nil
	case "waitForURL":
		var u string
		if json.Unmarshal(arg(req, 0), &u) != nil {
			return nil, invalid("waitForURL takes a URL string")
		}
		o, _ := options(arg(req, 1))
		if _, err := s.page(ctx, t, "waitForURL", u, waitOpts(o)); err != nil {
			return nil, s.fail(err, "")
		}
		return nil, nil
	case "evaluate":
		return s.evaluate(ctx, t, arg(req, 0), arg(req, 1), arg(req, 2))
	}
	return nil, invalid("tabs have no method " + strconv.Quote(req.Method))
}

func waitOpts(o tabOptions) map[string]any {
	out := map[string]any{}
	if o.TimeoutMs != nil {
		out["timeout"] = *o.TimeoutMs
	} else if o.Timeout != nil {
		out["timeout"] = *o.Timeout
	}
	return out
}

// pageInfo reads the tab's URL, title and viewport.
type viewport struct {
	URL   string  `json:"url"`
	Title string  `json:"title"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
}

func (s *Session) pageInfo(ctx context.Context, t *tabRec) (viewport, error) {
	v, err := s.page(ctx, t, "info")
	if err != nil {
		return viewport{}, err
	}
	var vp viewport
	_ = json.Unmarshal(v, &vp)
	t.url, t.title = vp.URL, vp.Title
	return vp, nil
}

// settle waits for the document after input, as the reference's reads do.
func (s *Session) settle(ctx context.Context, t *tabRec) {
	if !t.dirty {
		return
	}
	t.dirty = false
	_, _ = s.page(ctx, t, "waitForLoadState", "domcontentloaded", map[string]any{"timeout": 3000})
	select {
	case <-time.After(120 * time.Millisecond):
	case <-ctx.Done():
	}
}

// trees reads the accessibility tree, and the document when withDOM.
func (s *Session) trees(ctx context.Context, t *tabRec, withDOM bool) (*AXTree, *DOM, *protocol.Error) {
	s.settle(ctx, t)
	v, err := s.cdp(ctx, t, "Accessibility.getFullAXTree", nil)
	if err != nil {
		return nil, nil, s.fail(err, "")
	}
	tree, err := ParseAXTree(v)
	if err != nil {
		return nil, nil, s.fail(err, "")
	}
	if !withDOM {
		return tree, nil, nil
	}
	dv, err := s.cdp(ctx, t, "DOM.getDocument", map[string]any{"depth": -1, "pierce": true})
	if err != nil {
		return nil, nil, s.fail(err, "")
	}
	dom, err := ParseDOM(dv)
	if err != nil {
		return nil, nil, s.fail(err, "")
	}
	return tree, dom, nil
}

// readState reads a tab's state text through a binding. An initial read
// (a tab's first display) leaves no baseline, so the binding's first
// getAXState is full, as in the reference.
func (s *Session) readState(ctx context.Context, b *binding, disableDiffing, record bool) (string, *protocol.Error) {
	t := b.tab
	tree, dom, perr := s.trees(ctx, t, true)
	if perr != nil {
		return "", perr
	}
	if _, err := s.pageInfo(ctx, t); err != nil {
		return "", s.fail(err, "")
	}
	text := b.mem.Read(t.id, t.title, t.url, tree, DOMIDs(dom), !disableDiffing)
	if !record {
		// Ids and elements stay; only the baseline goes.
		b.mem.shown = map[int]axLine{}
	}
	return text, nil
}

// shoot captures the tab as PNG and records the screenshot's scale.
func (s *Session) shoot(ctx context.Context, t *tabRec, full bool) ([]byte, *protocol.Error) {
	s.settle(ctx, t)
	vp, err := s.pageInfo(ctx, t)
	if err != nil {
		return nil, s.fail(err, "")
	}
	dir := s.m.opts.Dir
	if dir == "" {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, "shot-"+randHex(6)+".png")
	defer os.Remove(path)
	opts := map[string]any{"path": path}
	if full {
		opts["fullPage"] = true
	}
	v, err := s.page(ctx, t, "screenshot", opts)
	if err != nil {
		return nil, s.fail(err, "")
	}
	var got string
	if json.Unmarshal(v, &got) == nil && got != "" && got != path {
		defer os.Remove(got)
		path = got
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, s.fail(fmt.Errorf("the screenshot did not arrive: %w", err), "")
	}
	if w := pngWidth(data); w > 0 && vp.W > 0 && !full {
		t.scale = vp.W / float64(w)
	}
	return data, nil
}

// pngWidth reads a PNG's width from its header.
func pngWidth(data []byte) int {
	if len(data) < 24 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
		return 0
	}
	return int(binary.BigEndian.Uint32(data[16:20]))
}

// point reads [x, y] in screenshot pixels as CSS pixels.
func (s *Session) point(t *tabRec, raw json.RawMessage) (float64, float64, *protocol.Error) {
	var p []float64
	if json.Unmarshal(raw, &p) != nil || len(p) != 2 {
		return 0, 0, invalid("a point is [x, y] in the tab screenshot's pixels")
	}
	scale := t.scale
	if scale <= 0 {
		scale = 1
	}
	return p[0] * scale, p[1] * scale, nil
}

// center scrolls an element into view and finds its centre in CSS pixels.
func (s *Session) center(ctx context.Context, t *tabRec, backend int) (float64, float64, error) {
	_, _ = s.cdp(ctx, t, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": backend})
	v, err := s.cdp(ctx, t, "DOM.getContentQuads", map[string]any{"backendNodeId": backend})
	if err == nil {
		var q struct {
			Quads [][]float64 `json:"quads"`
		}
		_ = json.Unmarshal(v, &q)
		for _, quad := range q.Quads {
			if len(quad) != 8 {
				continue
			}
			x := (quad[0] + quad[2] + quad[4] + quad[6]) / 4
			y := (quad[1] + quad[3] + quad[5] + quad[7]) / 4
			if math.Abs(quad[4]-quad[0]) >= 1 && math.Abs(quad[5]-quad[1]) >= 1 {
				return x, y, nil
			}
		}
	}
	v, err = s.cdp(ctx, t, "DOM.getBoxModel", map[string]any{"backendNodeId": backend})
	if err != nil {
		return 0, 0, errors.New("the element has no box on the page; it may be hidden. Read the state again")
	}
	var bm struct {
		Model struct {
			Content []float64 `json:"content"`
		} `json:"model"`
	}
	_ = json.Unmarshal(v, &bm)
	c := bm.Model.Content
	if len(c) != 8 {
		return 0, 0, errors.New("the element has no box on the page; it may be hidden. Read the state again")
	}
	return (c[0] + c[4]) / 2, (c[1] + c[5]) / 2, nil
}

// callOn runs a function with the element (a text node's parent element)
// as this and returns its JSON value.
func (s *Session) callOn(ctx context.Context, t *tabRec, backend int, fn string, args ...any) (json.RawMessage, error) {
	v, err := s.cdp(ctx, t, "DOM.resolveNode", map[string]any{"backendNodeId": backend})
	if err != nil {
		return nil, errors.New("the element is gone; read the state again")
	}
	var r struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	_ = json.Unmarshal(v, &r)
	callArgs := make([]map[string]any, len(args))
	for i, a := range args {
		callArgs[i] = map[string]any{"value": a}
	}
	v, err = s.cdp(ctx, t, "Runtime.callFunctionOn", map[string]any{
		"objectId": r.Object.ObjectID, "functionDeclaration": fn, "arguments": callArgs,
		"returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		return nil, err
	}
	return runtimeValue(v)
}

// runtimeValue reads a Runtime reply's value or its exception.
func runtimeValue(v json.RawMessage) (json.RawMessage, error) {
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
			Type  string          `json:"type"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(v, &res); err != nil {
		return nil, err
	}
	if res.Exception != nil {
		msg := res.Exception.Exception.Description
		if msg == "" {
			msg = res.Exception.Text
		}
		msg, _, _ = strings.Cut(msg, "\n    at ")
		return nil, errors.New(msg)
	}
	if len(res.Result.Value) == 0 {
		return json.RawMessage("null"), nil
	}
	return res.Result.Value, nil
}

const elementJS = "const el = this.nodeType === 1 ? this : this.parentElement;"

// focusElement focuses an element; one that cannot take focus is clicked.
func (s *Session) focusElement(ctx context.Context, t *tabRec, e Element) error {
	if _, err := s.cdp(ctx, t, "DOM.focus", map[string]any{"backendNodeId": e.Backend}); err == nil {
		return nil
	}
	x, y, err := s.center(ctx, t, e.Backend)
	if err != nil {
		return err
	}
	_, err = s.page(ctx, t, "mouse.click", x, y, map[string]any{"label": label("focus", e)})
	return err
}

// isSecret says whether the element, or the focused element when backend
// is zero, is a password field.
func (s *Session) isSecret(ctx context.Context, t *tabRec, backend int) bool {
	fn := "function() { " + elementJS + " return !!el && el.type === 'password'; }"
	var v json.RawMessage
	var err error
	if backend != 0 {
		v, err = s.callOn(ctx, t, backend, fn)
	} else {
		var r json.RawMessage
		r, err = s.cdp(ctx, t, "Runtime.evaluate", map[string]any{"expression": "(() => { const a = document.activeElement; return !!a && a.type === 'password'; })()", "returnByValue": true})
		if err == nil {
			v, err = runtimeValue(r)
		}
	}
	return err == nil && string(v) == "true"
}

// label is the short description ego shows beside its cursor.
func label(verb string, e Element) string {
	name := strings.Join(strings.Fields(e.Name), " ")
	if r := []rune(name); len(r) > 24 {
		name = string(r[:24])
	}
	out := verb + " " + e.Words
	if name != "" {
		out += " " + name
	}
	return out
}

var mouseButtons = map[string]string{"": "left", "left": "left", "l": "left", "right": "right", "r": "right", "middle": "middle", "m": "middle"}

func elementTarget(id int) *protocol.Target {
	return &protocol.Target{Ref: "element " + strconv.Itoa(id)}
}

func pointTarget(x, y float64) *protocol.Target {
	return &protocol.Target{Point: &protocol.Point{X: x, Y: y, Space: "tab"}}
}

// resolveTarget reads an element id or a point.
func (s *Session) resolveTarget(ctx context.Context, b *binding, raw json.RawMessage, verb string) (x, y float64, tgt *protocol.Target, lbl string, perr *protocol.Error) {
	t := b.tab
	var id float64
	if json.Unmarshal(raw, &id) == nil {
		if id != math.Trunc(id) || id < 0 {
			return 0, 0, nil, "", invalid("an element id is a whole number from the latest getAXState()")
		}
		e, perr := s.element(b, int(id))
		if perr != nil {
			return 0, 0, nil, "", perr
		}
		x, y, err := s.center(ctx, t, e.Backend)
		if err != nil {
			return 0, 0, nil, "", failed(fmt.Sprintf("element %d: %v", int(id), err))
		}
		return x, y, elementTarget(int(id)), label(verb, e), nil
	}
	x, y, perr = s.point(t, raw)
	if perr != nil {
		return 0, 0, nil, "", perr
	}
	return x, y, pointTarget(x, y), verb + " at point", nil
}

func (s *Session) click(ctx context.Context, b *binding, target, rawOpts json.RawMessage) (any, *protocol.Error) {
	t := b.tab
	o, perr := options(rawOpts)
	if perr != nil {
		return nil, perr
	}
	button, ok := mouseButtons[o.MouseButton]
	if !ok {
		return nil, invalid("mouseButton is left, right or middle (l, r, m)")
	}
	count := 1
	if o.ClickCount != nil {
		if *o.ClickCount < 1 {
			return nil, invalid("clickCount is a whole number from 1")
		}
		count = *o.ClickCount
	}
	hold, held, perr := duration(o)
	if perr != nil {
		return nil, perr
	}
	var mods []string
	if o.Key != "" {
		keys, err := PlaywrightKeys(o.Key)
		if err != nil {
			return nil, invalid(err.Error())
		}
		mods = keys
	}
	x, y, tgt, lbl, perr := s.resolveTarget(ctx, b, target, "click")
	if perr != nil {
		return nil, perr
	}
	step := protocol.Step{Click: tgt, Count: count}
	if button != "left" {
		step.Button = button
	}
	if o.Key != "" {
		step.Mods = strings.Split(o.Key, "+")
	}
	if perr := s.ask(ctx, step, t.title); perr != nil {
		return nil, perr
	}
	return nil, s.fail(s.clickAt(ctx, t, x, y, button, count, mods, hold, held, lbl), "")
}

// clickAt clicks at CSS pixels, holding modifier keys across the clicks
// and each press for hold when held.
func (s *Session) clickAt(ctx context.Context, t *tabRec, x, y float64, button string, count int, mods []string, hold time.Duration, held bool, lbl string) error {
	t.dirty = true
	for i, k := range mods {
		if _, err := s.page(ctx, t, "keyboard.down", k); err != nil {
			s.release(ctx, t, mods[:i])
			return err
		}
	}
	defer s.release(ctx, t, mods)
	if !held {
		_, err := s.page(ctx, t, "mouse.click", x, y, map[string]any{"button": button, "clickCount": count, "label": lbl})
		return err
	}
	if _, err := s.page(ctx, t, "mouse.move", x, y, map[string]any{"label": lbl}); err != nil {
		return err
	}
	for i := 1; i <= count; i++ {
		if _, err := s.page(ctx, t, "mouse.down", map[string]any{"button": button, "clickCount": i}); err != nil {
			return err
		}
		select {
		case <-time.After(hold):
		case <-ctx.Done():
		}
		if _, err := s.page(ctx, t, "mouse.up", map[string]any{"button": button, "clickCount": i}); err != nil {
			return err
		}
	}
	return nil
}

// release lets go of held keys, last first. It runs even after a stop.
func (s *Session) release(ctx context.Context, t *tabRec, keys []string) {
	rctx := context.WithoutCancel(ctx)
	for i := len(keys) - 1; i >= 0; i-- {
		_, _ = s.page(rctx, t, "keyboard.up", keys[i])
	}
}

func (s *Session) drag(ctx context.Context, b *binding, from, to json.RawMessage) (any, *protocol.Error) {
	t := b.tab
	x0, y0, perr := s.point(t, from)
	if perr != nil {
		return nil, perr
	}
	x1, y1, perr := s.point(t, to)
	if perr != nil {
		return nil, perr
	}
	if perr := s.ask(ctx, protocol.Step{Drag: pointTarget(x0, y0), To: pointTarget(x1, y1)}, t.title); perr != nil {
		return nil, perr
	}
	t.dirty = true
	err := func() error {
		if _, err := s.page(ctx, t, "mouse.move", x0, y0, map[string]any{"label": "drag"}); err != nil {
			return err
		}
		if _, err := s.page(ctx, t, "mouse.down"); err != nil {
			return err
		}
		_, err := s.page(ctx, t, "mouse.move", x1, y1, map[string]any{"steps": 12, "label": "drag"})
		if _, uerr := s.page(context.WithoutCancel(ctx), t, "mouse.up"); err == nil {
			err = uerr
		}
		return err
	}()
	return nil, s.fail(err, "")
}

var directions = map[string]string{"up": "up", "u": "up", "down": "down", "d": "down", "left": "left", "l": "left", "right": "right", "r": "right"}

func (s *Session) scroll(ctx context.Context, b *binding, target, rawDir, rawDist json.RawMessage) (any, *protocol.Error) {
	t := b.tab
	var d string
	_ = json.Unmarshal(rawDir, &d)
	dir, ok := directions[d]
	if !ok {
		return nil, invalid("direction is up, down, left or right (u, d, l, r)")
	}
	pages, pixels := 1.0, 0.0
	if !isNull(rawDist) {
		var n float64
		if json.Unmarshal(rawDist, &n) == nil {
			pages = n
		} else {
			o, perr := options(rawDist)
			if perr != nil || o.Pixels == nil {
				return nil, invalid("distance is a number of pages or { pixels }")
			}
			pixels = *o.Pixels
		}
	}
	x, y, tgt, lbl, perr := s.resolveTarget(ctx, b, target, "scroll")
	if perr != nil {
		return nil, perr
	}
	if perr := s.ask(ctx, protocol.Step{Scroll: tgt, Direction: dir}, t.title); perr != nil {
		return nil, perr
	}
	vp, err := s.pageInfo(ctx, t)
	if err != nil {
		return nil, s.fail(err, "")
	}
	amount := pixels
	if amount == 0 {
		size := vp.H
		if dir == "left" || dir == "right" {
			size = vp.W
		}
		if size <= 0 {
			size = 600
		}
		amount = pages * size * 0.9
	}
	dx, dy := 0.0, 0.0
	switch dir {
	case "up":
		dy = -amount
	case "down":
		dy = amount
	case "left":
		dx = -amount
	case "right":
		dx = amount
	}
	t.dirty = true
	if _, err := s.page(ctx, t, "mouse.move", x, y, map[string]any{"label": lbl}); err != nil {
		return nil, s.fail(err, "")
	}
	_, err = s.page(ctx, t, "mouse.wheel", dx, dy, map[string]any{"label": lbl})
	return nil, s.fail(err, "")
}

// utf16Offset converts a byte offset of s into UTF-16 units.
func utf16Offset(s string, byteOff int) int {
	return len(utf16.Encode([]rune(s[:byteOff])))
}

func (s *Session) selectText(ctx context.Context, b *binding, rawIdx, rawText, rawOpts json.RawMessage) (any, *protocol.Error) {
	t := b.tab
	id, _, perr := elementIndex(rawIdx, false)
	if perr != nil {
		return nil, perr
	}
	e, perr := s.element(b, id)
	if perr != nil {
		return nil, perr
	}
	var text string
	if json.Unmarshal(rawText, &text) != nil {
		return nil, invalid("selectText takes the text to select")
	}
	o, perr := options(rawOpts)
	if perr != nil {
		return nil, perr
	}
	v, err := s.callOn(ctx, t, e.Backend, "function() { "+elementJS+" return ('value' in el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')) ? el.value : el.textContent; }")
	if err != nil {
		return nil, s.fail(err, "")
	}
	var value string
	_ = json.Unmarshal(v, &value)
	var hits []int
	for i := strings.Index(value, text); i >= 0; {
		ok := (o.Prefix == "" || strings.HasSuffix(value[:i], o.Prefix)) && (o.Suffix == "" || strings.HasPrefix(value[i+len(text):], o.Suffix))
		if ok {
			hits = append(hits, i)
		}
		if text == "" {
			break
		}
		next := strings.Index(value[i+1:], text)
		if next < 0 {
			break
		}
		i += 1 + next
	}
	if len(hits) == 0 {
		extra := ""
		if o.Prefix != "" || o.Suffix != "" {
			extra = " with that prefix and suffix"
		}
		return nil, failed(fmt.Sprintf("%q is not in element %d%s", text, id, extra))
	}
	if len(hits) > 1 && o.Prefix == "" && o.Suffix == "" {
		return nil, failed(fmt.Sprintf("%q appears %d times in element %d; pass prefix or suffix to pick one", text, len(hits), id))
	}
	at := hits[0]
	start, end := utf16Offset(value, at), utf16Offset(value, at+len(text))
	switch o.SelectionType {
	case "", "text":
	case "cursor_before":
		end = start
	case "cursor_after":
		start = end
	default:
		return nil, invalid("selectionType is text, cursor_before or cursor_after")
	}
	if perr := s.ask(ctx, protocol.Step{SelectText: elementTarget(id)}, t.title); perr != nil {
		return nil, perr
	}
	t.dirty = true
	_, err = s.callOn(ctx, t, e.Backend, selectJS, start, end)
	return nil, s.fail(err, "")
}

const selectJS = `function(start, end) {
  const el = this.nodeType === 1 ? this : this.parentElement;
  el.focus();
  if ('value' in el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')) { el.setSelectionRange(start, end); return true; }
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  let pos = 0, sn = null, so = 0, en = null, eo = 0, n;
  while ((n = walker.nextNode())) {
    const len = n.nodeValue.length;
    if (!sn && start <= pos + len) { sn = n; so = start - pos; }
    if (!en && end <= pos + len) { en = n; eo = end - pos; break; }
    pos += len;
  }
  if (!sn || !en) throw new Error('the text is no longer there; read the state again');
  const r = document.createRange(); r.setStart(sn, so); r.setEnd(en, eo);
  const sel = getSelection(); sel.removeAllRanges(); sel.addRange(r); return true;
}`

const setValueJS = `function(v) {
  const el = this.nodeType === 1 ? this : this.parentElement;
  const fire = () => { el.dispatchEvent(new Event('input', { bubbles: true })); el.dispatchEvent(new Event('change', { bubbles: true })); };
  if (el.tagName === 'SELECT') {
    const o = [...el.options].find((o) => o.value === v || o.label === v || o.text.trim() === v);
    if (!o) throw new Error('no option ' + JSON.stringify(v) + ' in this list; it has: ' + [...el.options].map((o) => o.label).join(', '));
    el.value = o.value; fire(); return true;
  }
  if (el.tagName === 'INPUT' && (el.type === 'checkbox' || el.type === 'radio')) {
    const want = /^(1|true|on|yes|checked)$/i.test(v);
    if (el.checked !== want) el.click();
    return true;
  }
  if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') {
    el.focus();
    el.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true }));
    const proto = el.tagName === 'INPUT' ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
    Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, v);
    fire();
    el.dispatchEvent(new KeyboardEvent('keyup', { bubbles: true }));
    return true;
  }
  if (el.isContentEditable) { el.focus(); el.textContent = v; el.dispatchEvent(new Event('input', { bubbles: true })); return true; }
  throw new Error('this element takes no value; click it, or focus it and use typeText');
}`

func (s *Session) setValue(ctx context.Context, b *binding, rawIdx, rawValue json.RawMessage) (any, *protocol.Error) {
	t := b.tab
	id, _, perr := elementIndex(rawIdx, false)
	if perr != nil {
		return nil, perr
	}
	e, perr := s.element(b, id)
	if perr != nil {
		return nil, perr
	}
	var value string
	if json.Unmarshal(rawValue, &value) != nil {
		var any any
		if json.Unmarshal(rawValue, &any) != nil {
			return nil, invalid("setValue takes a string")
		}
		value = fmt.Sprint(any)
	}
	secret := s.isSecret(ctx, t, e.Backend)
	vraw := json.RawMessage(strconv.Quote(value))
	if secret {
		vraw = json.RawMessage(`"••••••"`)
	}
	if perr := s.ask(ctx, protocol.Step{Set: elementTarget(id), Value: &vraw}, t.title); perr != nil {
		return nil, perr
	}
	t.dirty = true
	_, err := s.callOn(ctx, t, e.Backend, setValueJS, value)
	return nil, s.fail(err, value)
}

func (s *Session) perform(ctx context.Context, b *binding, rawIdx, rawAction json.RawMessage) (any, *protocol.Error) {
	t := b.tab
	id, _, perr := elementIndex(rawIdx, false)
	if perr != nil {
		return nil, perr
	}
	e, perr := s.element(b, id)
	if perr != nil {
		return nil, perr
	}
	var action string
	_ = json.Unmarshal(rawAction, &action)
	name := ""
	for _, a := range e.Actions {
		if strings.EqualFold(a, action) {
			name = a
		}
	}
	if name == "" {
		listed := "; it lists none"
		if len(e.Actions) > 0 {
			listed = "; it lists: " + strings.Join(e.Actions, ", ")
		}
		return nil, failed(fmt.Sprintf("element %d has no secondary action %q%s", id, action, listed))
	}
	if perr := s.ask(ctx, protocol.Step{Perform: elementTarget(id), Action: name}, t.title); perr != nil {
		return nil, perr
	}
	// Expand and Collapse toggle the element, as a click does.
	x, y, err := s.center(ctx, t, e.Backend)
	if err != nil {
		return nil, failed(fmt.Sprintf("element %d: %v", id, err))
	}
	return nil, s.fail(s.clickAt(ctx, t, x, y, "left", 1, nil, 0, false, label(strings.ToLower(name), e)), "")
}

// focusIndex focuses the element an input method names, if any.
func (s *Session) focusIndex(ctx context.Context, b *binding, raw json.RawMessage) (int, *protocol.Target, *protocol.Error) {
	id, ok, perr := elementIndex(raw, true)
	if perr != nil {
		return 0, nil, perr
	}
	if !ok {
		return 0, nil, nil
	}
	e, perr := s.element(b, id)
	if perr != nil {
		return 0, nil, perr
	}
	return e.Backend, elementTarget(id), nil
}

func (s *Session) focusBackend(ctx context.Context, b *binding, backend int) *protocol.Error {
	if backend == 0 {
		return nil
	}
	var e Element
	for _, x := range b.mem.byID {
		if x.Backend == backend {
			e = x
		}
	}
	e.Backend = backend
	if err := s.focusElement(ctx, b.tab, e); err != nil {
		return s.fail(err, "")
	}
	return nil
}

func (s *Session) typeText(ctx context.Context, b *binding, rawIdx, rawText json.RawMessage) (any, *protocol.Error) {
	t := b.tab
	backend, into, perr := s.focusIndex(ctx, b, rawIdx)
	if perr != nil {
		return nil, perr
	}
	var text string
	if json.Unmarshal(rawText, &text) != nil {
		return nil, invalid("typeText takes the text to type")
	}
	secret := s.isSecret(ctx, t, backend)
	if perr := s.ask(ctx, protocol.Step{Type: &text, Into: into, Secret: secret}, t.title); perr != nil {
		return nil, perr
	}
	if perr := s.focusBackend(ctx, b, backend); perr != nil {
		return nil, perr
	}
	t.dirty = true
	_, err := s.page(ctx, t, "keyboard.type", text)
	return nil, s.fail(err, text)
}

func (s *Session) pressKey(ctx context.Context, b *binding, rawIdx, rawKey, rawOpts json.RawMessage) (any, *protocol.Error) {
	t := b.tab
	backend, _, perr := s.focusIndex(ctx, b, rawIdx)
	if perr != nil {
		return nil, perr
	}
	var key string
	if json.Unmarshal(rawKey, &key) != nil {
		return nil, invalid(`pressKey takes a key such as "Return" or "super+c"`)
	}
	keys, err := PlaywrightKeys(key)
	if err != nil {
		return nil, invalid(err.Error())
	}
	o, perr := options(rawOpts)
	if perr != nil {
		return nil, perr
	}
	hold, held, perr := duration(o)
	if perr != nil {
		return nil, perr
	}
	if perr := s.ask(ctx, protocol.Step{Key: key}, t.title); perr != nil {
		return nil, perr
	}
	if perr := s.focusBackend(ctx, b, backend); perr != nil {
		return nil, perr
	}
	t.dirty = true
	if !held {
		_, err := s.page(ctx, t, "keyboard.press", strings.Join(keys, "+"))
		return nil, s.fail(err, "")
	}
	for i, k := range keys {
		if _, err := s.page(ctx, t, "keyboard.down", k); err != nil {
			s.release(ctx, t, keys[:i])
			return nil, s.fail(err, "")
		}
	}
	select {
	case <-time.After(hold):
	case <-ctx.Done():
	}
	s.release(ctx, t, keys)
	return nil, nil
}

func (s *Session) paste(ctx context.Context, b *binding, rawIdx, rawText, rawOpts json.RawMessage) (any, *protocol.Error) {
	t := b.tab
	backend, into, perr := s.focusIndex(ctx, b, rawIdx)
	if perr != nil {
		return nil, perr
	}
	var text string
	if json.Unmarshal(rawText, &text) != nil {
		return nil, invalid("paste takes the text to paste")
	}
	o, perr := options(rawOpts)
	if perr != nil {
		return nil, perr
	}
	var content any = text
	switch o.Format {
	case "", "text", "md":
	case "html":
		content = map[string]any{"text": text, "html": text}
	default:
		return nil, invalid("format is text, md or html")
	}
	secret := s.isSecret(ctx, t, backend)
	if perr := s.ask(ctx, protocol.Step{Type: &text, Into: into, Secret: secret}, t.title); perr != nil {
		return nil, perr
	}
	if perr := s.focusBackend(ctx, b, backend); perr != nil {
		return nil, perr
	}
	t.dirty = true
	_, err := s.page(ctx, t, "keyboard.paste", content)
	return nil, s.fail(err, text)
}

func (s *Session) history(ctx context.Context, t *tabRec, back bool) (any, *protocol.Error) {
	v, err := s.cdp(ctx, t, "Page.getNavigationHistory", nil)
	if err != nil {
		return nil, s.fail(err, "")
	}
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	_ = json.Unmarshal(v, &h)
	i := h.CurrentIndex + 1
	if back {
		i = h.CurrentIndex - 1
	}
	if i < 0 || i >= len(h.Entries) {
		where := "later"
		if back {
			where = "earlier"
		}
		return nil, failed("this tab has no " + where + " page in its history")
	}
	t.dirty = true
	if _, err := s.cdp(ctx, t, "Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[i].ID}); err != nil {
		return nil, s.fail(err, "")
	}
	_, _ = s.page(ctx, t, "waitForLoadState", "load", map[string]any{"timeout": 10000})
	return nil, nil
}

// evaluate runs page code: a function's source (called with arg) or an
// expression.
func (s *Session) evaluate(ctx context.Context, t *tabRec, rawSrc, rawIsFn, rawArg json.RawMessage) (any, *protocol.Error) {
	var src string
	if json.Unmarshal(rawSrc, &src) != nil || strings.TrimSpace(src) == "" {
		return nil, invalid("evaluate takes a function or an expression")
	}
	var isFn bool
	_ = json.Unmarshal(rawIsFn, &isFn)
	expr := src
	if isFn {
		a := "undefined"
		if !isNull(rawArg) {
			a = string(rawArg)
		}
		expr = "(" + src + ")(" + a + ")"
	}
	// Page code can act on the page: confirm mode "every" asks first.
	if perr := s.ask(ctx, protocol.Step{Perform: &protocol.Target{Select: "page script"}, Action: "evaluate"}, t.title); perr != nil {
		return nil, perr
	}
	t.dirty = true
	v, err := s.cdp(ctx, t, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true, "userGesture": true})
	if err != nil {
		return nil, s.fail(err, "")
	}
	val, err := runtimeValue(v)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.ErrInternal, Message: err.Error()}
	}
	var out any
	_ = json.Unmarshal(val, &out)
	return map[string]any{"value": out}, nil
}
