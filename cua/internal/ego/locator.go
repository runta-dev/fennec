package ego

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"fennec/cua/internal/protocol"
)

// The tab.playwright locator subset (docs/codex-parity.md, section 8):
// locators are resolved here against the page's accessibility tree and
// document, then acted on with the same element operations as element ids.

// matcher is a text matcher: a string (substring, case-insensitive,
// whitespace-normalised unless exact) or a RegExp's source and flags.
type matcher struct {
	S      *string `json:"s"`
	Source string  `json:"source"`
	Flags  string  `json:"flags"`
}

func normSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func (m *matcher) compile(exact bool) (func(string) bool, error) {
	if m == nil {
		return func(string) bool { return true }, nil
	}
	if m.S != nil {
		want := normSpace(*m.S)
		if exact {
			return func(s string) bool { return normSpace(s) == want }, nil
		}
		lw := strings.ToLower(want)
		return func(s string) bool { return strings.Contains(strings.ToLower(normSpace(s)), lw) }, nil
	}
	flags := ""
	for _, f := range m.Flags {
		if f == 'i' || f == 'm' || f == 's' {
			flags += string(f)
		}
	}
	src := m.Source
	if flags != "" {
		src = "(?" + flags + ")" + src
	}
	re, err := regexp.Compile(src)
	if err != nil {
		return nil, fmt.Errorf("the regular expression /%s/ is not supported here: %v", m.Source, err)
	}
	return re.MatchString, nil
}

// locStep is one link of a locator chain.
type locStep struct {
	Kind     string    `json:"kind"` // css, text, role, label, placeholder, testid, alt, title, nth, filter, and, or
	Selector string    `json:"selector"`
	Text     *matcher  `json:"text"`
	Exact    bool      `json:"exact"`
	Role     string    `json:"role"`
	Name     *matcher  `json:"name"`
	Checked  *bool     `json:"checked"`
	Selected *bool     `json:"selected"`
	Expanded *bool     `json:"expanded"`
	Disabled *bool     `json:"disabled"`
	Pressed  *bool     `json:"pressed"`
	Level    *int      `json:"level"`
	N        int       `json:"n"`
	HasText  *matcher  `json:"hasText"`
	NotText  *matcher  `json:"hasNotText"`
	Has      []locStep `json:"has"`
	HasNot   []locStep `json:"hasNot"`
	Visible  *bool     `json:"visible"`
	Other    []locStep `json:"other"`
}

// page facts one resolution reads once.
type pageFacts struct {
	s      *Session
	t      *tabRec
	ax     *AXTree
	dom    *DOM
	byNode map[int]*DOMNode // DOM nodeId -> node
	order  map[int]int      // backend -> document position
	axOf   map[int]*AXNode
}

func (s *Session) facts(ctx context.Context, t *tabRec) (*pageFacts, *protocol.Error) {
	ax, dom, perr := s.trees(ctx, t, true)
	if perr != nil {
		return nil, perr
	}
	f := &pageFacts{s: s, t: t, ax: ax, dom: dom, byNode: map[int]*DOMNode{}, order: map[int]int{}, axOf: map[int]*AXNode{}}
	for i, n := range dom.Order {
		f.byNode[n.NodeID] = n
		f.order[n.Backend] = i
	}
	for _, n := range ax.Nodes {
		if n.Backend != 0 && !n.Ignored {
			if _, ok := f.axOf[n.Backend]; !ok {
				f.axOf[n.Backend] = n
			}
		}
	}
	return f, nil
}

// ariaRole is a node's role as getByRole names it.
func ariaRole(n *AXNode) string {
	switch n.Role {
	case "image":
		return "img"
	case "StaticText", "InlineTextBox", "LineBreak", "RootWebArea", "WebArea":
		return ""
	}
	return strings.ToLower(n.Role)
}

func (f *pageFacts) within(scopes []int, backend int) bool {
	for _, sc := range scopes {
		if f.dom.Contains(sc, backend) {
			return true
		}
	}
	return false
}

// textOf is an element's text: its text nodes in document order.
func (f *pageFacts) textOf(backend int) string {
	n := f.dom.ByBackend[backend]
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(n *DOMNode)
	walk = func(n *DOMNode) {
		if n.Type == 3 {
			b.WriteString(n.Value)
		}
		if n.Name == "script" || n.Name == "style" {
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func (f *pageFacts) sortDoc(ids []int) []int {
	seen := map[int]bool{}
	out := ids[:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return f.order[out[i]] < f.order[out[j]] })
	return out
}

func (f *pageFacts) attrMatch(scopes []int, attr string, m *matcher, exact bool) ([]int, error) {
	match, err := m.compile(exact)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, n := range f.dom.Order {
		if v, ok := n.Attrs[attr]; ok && n.Type == 1 && match(v) && f.within(scopes, n.Backend) {
			out = append(out, n.Backend)
		}
	}
	return out, nil
}

// resolve runs a chain from scopes (backend ids; the document at first).
func (f *pageFacts) resolve(ctx context.Context, chain []locStep, scopes []int) ([]int, error) {
	cur := scopes
	for _, st := range chain {
		var next []int
		var err error
		switch st.Kind {
		case "css":
			next, err = f.css(ctx, st.Selector, cur)
		case "text":
			next, err = f.text(cur, st.Text, st.Exact)
		case "role":
			next, err = f.role(cur, st)
		case "label":
			next, err = f.label(cur, st.Text, st.Exact)
		case "placeholder":
			next, err = f.attrMatch(cur, "placeholder", st.Text, st.Exact)
		case "alt":
			next, err = f.attrMatch(cur, "alt", st.Text, st.Exact)
		case "title":
			next, err = f.attrMatch(cur, "title", st.Text, st.Exact)
		case "testid":
			var s string
			if st.Text != nil && st.Text.S != nil {
				s = *st.Text.S
			}
			for _, n := range f.dom.Order {
				if n.Type == 1 && n.Attrs["data-testid"] == s && f.within(cur, n.Backend) {
					next = append(next, n.Backend)
				}
			}
		case "nth":
			i := st.N
			if i < 0 {
				i = len(cur) + i
			}
			if i >= 0 && i < len(cur) {
				next = []int{cur[i]}
			}
		case "filter":
			next, err = f.filter(ctx, cur, st)
		case "and", "or":
			var other []int
			other, err = f.resolve(ctx, st.Other, []int{f.dom.Root.Backend})
			if err == nil {
				in := map[int]bool{}
				for _, o := range other {
					in[o] = true
				}
				if st.Kind == "and" {
					for _, c := range cur {
						if in[c] {
							next = append(next, c)
						}
					}
				} else {
					next = append(append(next, cur...), other...)
				}
			}
		default:
			err = fmt.Errorf("locator step %q is not supported", st.Kind)
		}
		if err != nil {
			return nil, err
		}
		cur = f.sortDoc(next)
		if st.Kind == "nth" {
			cur = next
		}
	}
	return cur, nil
}

func (f *pageFacts) css(ctx context.Context, sel string, scopes []int) ([]int, error) {
	sel = strings.TrimSpace(sel)
	switch {
	case strings.HasPrefix(sel, "css="):
		sel = sel[4:]
	case strings.HasPrefix(sel, "text="):
		t := sel[5:]
		exact := len(t) >= 2 && t[0] == '"' && t[len(t)-1] == '"'
		if exact {
			t = t[1 : len(t)-1]
		}
		return f.text(scopes, &matcher{S: &t}, exact)
	case strings.HasPrefix(sel, "xpath=") || strings.HasPrefix(sel, "//") || strings.HasPrefix(sel, "(//"):
		return f.xpath(ctx, strings.TrimPrefix(sel, "xpath="), scopes)
	case strings.HasPrefix(sel, "internal:") || strings.Contains(sel, ">>"):
		return nil, fmt.Errorf("the selector %q is not supported; use CSS, text= or xpath=, or chain locator calls", sel)
	}
	var out []int
	for _, sc := range scopes {
		n := f.dom.ByBackend[sc]
		if n == nil {
			continue
		}
		v, err := f.s.cdp(ctx, f.t, "DOM.querySelectorAll", map[string]any{"nodeId": n.NodeID, "selector": sel})
		if err != nil {
			return nil, fmt.Errorf("the selector %q does not parse: %v", sel, err)
		}
		var r struct {
			NodeIDs []int `json:"nodeIds"`
		}
		_ = json.Unmarshal(v, &r)
		for _, id := range r.NodeIDs {
			if dn := f.byNode[id]; dn != nil {
				out = append(out, dn.Backend)
			}
		}
	}
	return out, nil
}

func (f *pageFacts) xpath(ctx context.Context, expr string, scopes []int) ([]int, error) {
	v, err := f.s.cdp(ctx, f.t, "DOM.performSearch", map[string]any{"query": expr})
	if err != nil {
		return nil, err
	}
	var r struct {
		SearchID    string `json:"searchId"`
		ResultCount int    `json:"resultCount"`
	}
	_ = json.Unmarshal(v, &r)
	defer func() { _, _ = f.s.cdp(ctx, f.t, "DOM.discardSearchResults", map[string]any{"searchId": r.SearchID}) }()
	if r.ResultCount == 0 {
		return nil, nil
	}
	v, err = f.s.cdp(ctx, f.t, "DOM.getSearchResults", map[string]any{"searchId": r.SearchID, "fromIndex": 0, "toIndex": r.ResultCount})
	if err != nil {
		return nil, err
	}
	var res struct {
		NodeIDs []int `json:"nodeIds"`
	}
	_ = json.Unmarshal(v, &res)
	var out []int
	for _, id := range res.NodeIDs {
		if dn := f.byNode[id]; dn != nil && f.within(scopes, dn.Backend) {
			out = append(out, dn.Backend)
		}
	}
	return out, nil
}

// text finds the smallest elements whose text matches.
func (f *pageFacts) text(scopes []int, m *matcher, exact bool) ([]int, error) {
	match, err := m.compile(exact)
	if err != nil {
		return nil, err
	}
	var out []int
	var walk func(n *DOMNode) bool
	walk = func(n *DOMNode) bool {
		if n.Type != 1 && n.Type != 9 && n.Type != 11 {
			return false
		}
		if n.Name == "script" || n.Name == "style" || n.Name == "head" || n.Name == "title" {
			return false
		}
		child := false
		for _, c := range n.Children {
			if walk(c) {
				child = true
			}
		}
		if child {
			return true
		}
		if n.Type == 1 && n.Name != "html" && n.Name != "body" && match(f.textOf(n.Backend)) {
			if f.within(scopes, n.Backend) {
				out = append(out, n.Backend)
			}
			return true
		}
		return false
	}
	walk(f.dom.Root)
	return out, nil
}

func (f *pageFacts) role(scopes []int, st locStep) ([]int, error) {
	want := strings.ToLower(strings.TrimSpace(st.Role))
	if want == "" {
		return nil, errors.New("getByRole takes a role")
	}
	match, err := st.Name.compile(st.Exact)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, n := range f.ax.Nodes {
		if n.Ignored || n.Backend == 0 || ariaRole(n) != want {
			continue
		}
		if st.Name != nil && !match(n.Name) {
			continue
		}
		if st.Checked != nil && (n.Props["checked"] == "true") != *st.Checked {
			continue
		}
		if st.Selected != nil && n.True("selected") != *st.Selected {
			continue
		}
		if st.Expanded != nil && n.True("expanded") != *st.Expanded {
			continue
		}
		if st.Disabled != nil && n.True("disabled") != *st.Disabled {
			continue
		}
		if st.Pressed != nil && (n.Props["pressed"] == "true") != *st.Pressed {
			continue
		}
		if st.Level != nil && n.Props["level"] != fmt.Sprint(*st.Level) {
			continue
		}
		if f.within(scopes, n.Backend) {
			out = append(out, n.Backend)
		}
	}
	return out, nil
}

var labelledRoles = map[string]bool{"textbox": true, "searchbox": true, "combobox": true, "checkbox": true, "radio": true, "switch": true, "slider": true, "spinbutton": true, "listbox": true}

func (f *pageFacts) label(scopes []int, m *matcher, exact bool) ([]int, error) {
	match, err := m.compile(exact)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, n := range f.ax.Nodes {
		if n.Ignored || n.Backend == 0 || !labelledRoles[n.Role] {
			continue
		}
		if (n.NameFrom == "relatedElement" || n.NameFrom == "attribute") && match(n.Name) && f.within(scopes, n.Backend) {
			out = append(out, n.Backend)
		}
	}
	return out, nil
}

func (f *pageFacts) filter(ctx context.Context, cur []int, st locStep) ([]int, error) {
	has, err := st.HasText.compile(false)
	if err != nil {
		return nil, err
	}
	hasNot, err := st.NotText.compile(false)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, c := range cur {
		text := f.textOf(c)
		if st.HasText != nil && !has(text) {
			continue
		}
		if st.NotText != nil && hasNot(text) {
			continue
		}
		if len(st.Has) > 0 {
			sub, err := f.resolve(ctx, st.Has, []int{c})
			if err != nil {
				return nil, err
			}
			if len(withoutSelf(sub, c)) == 0 {
				continue
			}
		}
		if len(st.HasNot) > 0 {
			sub, err := f.resolve(ctx, st.HasNot, []int{c})
			if err != nil {
				return nil, err
			}
			if len(withoutSelf(sub, c)) > 0 {
				continue
			}
		}
		if st.Visible != nil {
			_, _, err := f.s.center(ctx, f.t, c)
			if (err == nil) != *st.Visible {
				continue
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func withoutSelf(ids []int, self int) []int {
	out := ids[:0:0]
	for _, id := range ids {
		if id != self {
			out = append(out, id)
		}
	}
	return out
}

// locOptions holds every option a locator action takes.
type locOptions struct {
	Button    string   `json:"button"`
	Modifiers []string `json:"modifiers"`
	ClickCnt  *int     `json:"clickCount"`
	TimeoutMs *float64 `json:"timeoutMs"`
	Timeout   *float64 `json:"timeout"`
	State     string   `json:"state"`
	Force     bool     `json:"force"`
}

// DefaultLocatorTimeout bounds a locator action's wait for its element.
const DefaultLocatorTimeout = 5 * time.Second

var pwModifiers = map[string]string{"alt": "Alt", "control": "Control", "meta": "Meta", "shift": "Shift", "controlormeta": "Meta"}

func (s *Session) locCall(ctx context.Context, req request) (any, *protocol.Error) {
	b, perr := s.bindingOf(req)
	if perr != nil {
		return nil, perr
	}
	t := b.tab
	var o locOptions
	optArg := map[string]int{"click": 0, "dblclick": 0, "hover": 0, "check": 0, "uncheck": 0, "focus": 0, "waitFor": 0,
		"fill": 1, "press": 1, "type": 1, "pressSequentially": 1, "selectOption": 1, "setChecked": 1, "getAttribute": 1,
		"innerText": 0, "textContent": 0, "inputValue": 0, "clear": 0, "scrollIntoViewIfNeeded": 0}
	if i, ok := optArg[req.Method]; ok && !isNull(arg(req, i)) {
		if err := json.Unmarshal(arg(req, i), &o); err != nil {
			return nil, invalid("bad options: " + err.Error())
		}
	}
	timeout := DefaultLocatorTimeout
	if o.TimeoutMs != nil {
		timeout = time.Duration(*o.TimeoutMs) * time.Millisecond
	} else if o.Timeout != nil {
		timeout = time.Duration(*o.Timeout) * time.Millisecond
	}
	desc := req.Desc
	if desc == "" {
		desc = "the locator"
	}
	// find resolves the chain once.
	find := func() ([]int, *pageFacts, *protocol.Error) {
		f, perr := s.facts(ctx, t)
		if perr != nil {
			return nil, nil, perr
		}
		ids, err := f.resolve(ctx, req.Chain, []int{f.dom.Root.Backend})
		if err != nil {
			return nil, nil, invalid(err.Error())
		}
		return ids, f, nil
	}
	// one waits for exactly one element.
	one := func() (int, *pageFacts, *protocol.Error) {
		deadline := time.Now().Add(timeout)
		for {
			ids, f, perr := find()
			if perr != nil {
				return 0, nil, perr
			}
			if len(ids) > 1 {
				return 0, nil, &protocol.Error{Code: protocol.ErrTargetAmbiguous, Message: fmt.Sprintf("strict mode violation: %s resolved to %d elements", desc, len(ids))}
			}
			if len(ids) == 1 {
				return ids[0], f, nil
			}
			if time.Now().After(deadline) {
				return 0, nil, &protocol.Error{Code: protocol.ErrTimeout, Message: fmt.Sprintf("Timeout %dms exceeded waiting for %s", timeout.Milliseconds(), desc)}
			}
			select {
			case <-time.After(150 * time.Millisecond):
			case <-ctx.Done():
				return 0, nil, s.fail(ctx.Err(), "")
			}
		}
	}
	el := func(backend int, f *pageFacts) Element {
		e := Element{Backend: backend}
		if n := f.axOf[backend]; n != nil {
			e.Words, e.Name = roleWords(n), n.Name
		}
		return e
	}
	strArg := func(i int, what string) (string, *protocol.Error) {
		var v string
		if json.Unmarshal(arg(req, i), &v) != nil {
			return "", invalid(what)
		}
		return v, nil
	}
	target := &protocol.Target{Select: desc}
	switch req.Method {
	case "count":
		ids, _, perr := find()
		if perr != nil {
			return nil, perr
		}
		return len(ids), nil
	case "allTextContents", "allInnerTexts":
		ids, f, perr := find()
		if perr != nil {
			return nil, perr
		}
		out := []string{}
		for _, id := range ids {
			if req.Method == "allTextContents" {
				out = append(out, f.textOf(id))
				continue
			}
			v, err := s.callOn(ctx, t, id, "function() { "+elementJS+" return el.innerText; }")
			if err != nil {
				return nil, s.fail(err, "")
			}
			var x string
			_ = json.Unmarshal(v, &x)
			out = append(out, x)
		}
		return out, nil
	case "isVisible", "isHidden":
		ids, _, perr := find()
		if perr != nil {
			return nil, perr
		}
		vis := false
		if len(ids) > 1 {
			return nil, &protocol.Error{Code: protocol.ErrTargetAmbiguous, Message: fmt.Sprintf("strict mode violation: %s resolved to %d elements", desc, len(ids))}
		}
		if len(ids) == 1 {
			v, err := s.callOn(ctx, t, ids[0], "function() { "+elementJS+" const r = el.getBoundingClientRect(); const st = getComputedStyle(el); return r.width > 0 && r.height > 0 && st.visibility !== 'hidden'; }")
			vis = err == nil && string(v) == "true"
		}
		if req.Method == "isHidden" {
			return !vis, nil
		}
		return vis, nil
	case "waitFor":
		state := o.State
		if state == "" {
			state = "visible"
		}
		deadline := time.Now().Add(timeout)
		for {
			ids, _, perr := find()
			if perr != nil {
				return nil, perr
			}
			ok := false
			switch state {
			case "attached", "visible":
				ok = len(ids) > 0
			case "detached", "hidden":
				ok = len(ids) == 0
			default:
				return nil, invalid("state is attached, detached, visible or hidden")
			}
			if ok {
				return nil, nil
			}
			if time.Now().After(deadline) {
				return nil, &protocol.Error{Code: protocol.ErrTimeout, Message: fmt.Sprintf("Timeout %dms exceeded waiting for %s to be %s", timeout.Milliseconds(), desc, state)}
			}
			select {
			case <-time.After(150 * time.Millisecond):
			case <-ctx.Done():
				return nil, s.fail(ctx.Err(), "")
			}
		}
	}
	id, f, perr := one()
	if perr != nil {
		return nil, perr
	}
	e := el(id, f)
	switch req.Method {
	case "click", "dblclick", "hover":
		button, ok := mouseButtons[o.Button]
		if !ok {
			return nil, invalid("button is left, right or middle")
		}
		var mods []string
		for _, m := range o.Modifiers {
			k, ok := pwModifiers[strings.ToLower(m)]
			if !ok {
				return nil, invalid("modifiers are Alt, Control, ControlOrMeta, Meta and Shift")
			}
			mods = append(mods, k)
		}
		count := 1
		if req.Method == "dblclick" {
			count = 2
		} else if o.ClickCnt != nil && *o.ClickCnt > 1 {
			count = *o.ClickCnt
		}
		step := protocol.Step{Click: target, Count: count, Mods: o.Modifiers}
		if button != "left" {
			step.Button = button
		}
		x, y, err := s.center(ctx, t, id)
		if err != nil {
			return nil, failed(desc + ": " + err.Error())
		}
		if req.Method == "hover" {
			_, err = s.page(ctx, t, "mouse.move", x, y, map[string]any{"label": label("hover", e)})
			return nil, s.fail(err, "")
		}
		if perr := s.ask(ctx, step, t.title); perr != nil {
			return nil, perr
		}
		return nil, s.fail(s.clickAt(ctx, t, x, y, button, count, mods, 0, false, label("click", e)), "")
	case "check", "uncheck", "setChecked":
		want := req.Method != "uncheck"
		if req.Method == "setChecked" {
			if json.Unmarshal(arg(req, 0), &want) != nil {
				return nil, invalid("setChecked takes true or false")
			}
		}
		read := func() (bool, error) {
			v, err := s.callOn(ctx, t, id, "function() { "+elementJS+" return 'checked' in el && el.tagName === 'INPUT' ? el.checked : el.getAttribute('aria-checked') === 'true'; }")
			return string(v) == "true", err
		}
		got, err := read()
		if err != nil {
			return nil, s.fail(err, "")
		}
		if got == want {
			return nil, nil
		}
		raw := json.RawMessage("true")
		if !want {
			raw = json.RawMessage("false")
		}
		if perr := s.ask(ctx, protocol.Step{Set: target, Value: &raw}, t.title); perr != nil {
			return nil, perr
		}
		x, y, err := s.center(ctx, t, id)
		if err != nil {
			return nil, failed(desc + ": " + err.Error())
		}
		if err := s.clickAt(ctx, t, x, y, "left", 1, nil, 0, false, label("check", e)); err != nil {
			return nil, s.fail(err, "")
		}
		if got, _ = read(); got != want {
			return nil, failed(desc + " did not change its checked state")
		}
		return nil, nil
	case "fill", "clear":
		value := ""
		if req.Method == "fill" {
			v, perr := strArg(0, "fill takes the value to fill")
			if perr != nil {
				return nil, perr
			}
			value = v
		}
		secret := s.isSecret(ctx, t, id)
		shown := json.RawMessage(fmt.Sprintf("%q", value))
		if secret {
			shown = json.RawMessage(`"••••••"`)
		}
		if perr := s.ask(ctx, protocol.Step{Set: target, Value: &shown}, t.title); perr != nil {
			return nil, perr
		}
		t.dirty = true
		if _, err := s.callOn(ctx, t, id, "function() { "+elementJS+" el.focus(); if (typeof el.select === 'function' && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')) { el.select(); } else if (el.isContentEditable) { const r = document.createRange(); r.selectNodeContents(el); const s = getSelection(); s.removeAllRanges(); s.addRange(r); } else { throw new Error('the element is not an input, a text area or editable'); } return true; }"); err != nil {
			return nil, s.fail(err, value)
		}
		var err error
		if value == "" {
			_, err = s.page(ctx, t, "keyboard.press", "Delete")
		} else {
			_, err = s.page(ctx, t, "keyboard.insertText", value)
		}
		return nil, s.fail(err, value)
	case "press", "type", "pressSequentially":
		v, perr := strArg(0, req.Method+" takes text")
		if perr != nil {
			return nil, perr
		}
		// press takes Playwright key names, which ego's keyboard reads.
		step := protocol.Step{Key: v}
		secret := false
		if req.Method != "press" {
			secret = s.isSecret(ctx, t, id)
			step = protocol.Step{Type: &v, Into: target, Secret: secret}
		}
		if perr := s.ask(ctx, step, t.title); perr != nil {
			return nil, perr
		}
		if err := s.focusElement(ctx, t, e); err != nil {
			return nil, s.fail(err, "")
		}
		t.dirty = true
		var err error
		if req.Method == "press" {
			_, err = s.page(ctx, t, "keyboard.press", v)
			return nil, s.fail(err, "")
		}
		_, err = s.page(ctx, t, "keyboard.type", v)
		return nil, s.fail(err, v)
	case "selectOption":
		var vals []any
		raw := arg(req, 0)
		if json.Unmarshal(raw, &vals) != nil {
			var one any
			if json.Unmarshal(raw, &one) != nil {
				return nil, invalid("selectOption takes a value, a label, { value | label | index } or a list of them")
			}
			vals = []any{one}
		}
		shown := json.RawMessage(raw)
		if perr := s.ask(ctx, protocol.Step{Set: target, Value: &shown}, t.title); perr != nil {
			return nil, perr
		}
		t.dirty = true
		v, err := s.callOn(ctx, t, id, selectOptionJS, vals)
		if err != nil {
			return nil, s.fail(err, "")
		}
		var out any
		_ = json.Unmarshal(v, &out)
		return out, nil
	case "focus":
		return nil, s.fail(s.focusElement(ctx, t, e), "")
	case "scrollIntoViewIfNeeded":
		_, err := s.cdp(ctx, t, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": id})
		return nil, s.fail(err, "")
	case "innerText", "textContent", "inputValue", "isEnabled", "isDisabled", "isChecked", "isEditable", "boundingBox":
		fns := map[string]string{
			"innerText":   "return el.innerText;",
			"textContent": "return this.textContent;",
			"inputValue":  "if (!('value' in el)) throw new Error('the element is not an input, a text area or a select'); return el.value;",
			"isEnabled":   "return !el.disabled && el.getAttribute('aria-disabled') !== 'true';",
			"isDisabled":  "return !!el.disabled || el.getAttribute('aria-disabled') === 'true';",
			"isChecked":   "return 'checked' in el && el.tagName === 'INPUT' ? el.checked : el.getAttribute('aria-checked') === 'true';",
			"isEditable":  "return !el.disabled && !el.readOnly && (el.isContentEditable || el.tagName === 'INPUT' || el.tagName === 'TEXTAREA');",
			"boundingBox": "const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height };",
		}
		v, err := s.callOn(ctx, t, id, "function() { "+elementJS+" "+fns[req.Method]+" }")
		if err != nil {
			return nil, s.fail(err, "")
		}
		var out any
		_ = json.Unmarshal(v, &out)
		return out, nil
	case "getAttribute":
		name, perr := strArg(0, "getAttribute takes an attribute name")
		if perr != nil {
			return nil, perr
		}
		v, err := s.callOn(ctx, t, id, "function(n) { "+elementJS+" return el.getAttribute(n); }", name)
		if err != nil {
			return nil, s.fail(err, "")
		}
		var out any
		_ = json.Unmarshal(v, &out)
		return out, nil
	case "evaluate":
		src, perr := strArg(0, "evaluate takes a function")
		if perr != nil {
			return nil, perr
		}
		var argv any
		_ = json.Unmarshal(arg(req, 1), &argv)
		if perr := s.ask(ctx, protocol.Step{Perform: target, Action: "evaluate"}, t.title); perr != nil {
			return nil, perr
		}
		t.dirty = true
		v, err := s.callOn(ctx, t, id, "function(arg) { "+elementJS+" return ("+src+")(el, arg); }", argv)
		if err != nil {
			return nil, &protocol.Error{Code: protocol.ErrInternal, Message: err.Error()}
		}
		var out any
		_ = json.Unmarshal(v, &out)
		return map[string]any{"value": out}, nil
	}
	return nil, invalid("locators have no method " + req.Method)
}

const selectOptionJS = `function(vals) {
  const el = this.nodeType === 1 ? this : this.parentElement;
  if (el.tagName !== 'SELECT') throw new Error('the element is not a <select>');
  const opts = [...el.options];
  const picked = vals.map((v) => {
    const o = (v && typeof v === 'object')
      ? opts.find((o, i) => (v.value !== undefined && o.value === v.value) || (v.label !== undefined && o.label === v.label) || (v.index !== undefined && i === v.index))
      : opts.find((o) => o.value === String(v) || o.label === String(v));
    if (!o) throw new Error('no option ' + JSON.stringify(v) + '; the list has: ' + opts.map((o) => o.label).join(', '));
    return o;
  });
  opts.forEach((o) => { o.selected = picked.includes(o); });
  el.dispatchEvent(new Event('input', { bubbles: true }));
  el.dispatchEvent(new Event('change', { bubbles: true }));
  return picked.map((o) => o.value);
}`
