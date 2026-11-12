package view

import (
	"context"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"fennec/cua/internal/protocol"
)

// Observer assembles observe answers from a Source. Images and vision
// marks are the caller's.
type Observer struct {
	Source Source
	Refs   *Registry
	// Input is the configured input mode (foreground, background, auto).
	Input string
	// URLs attaches link targets to outlines, nodes and find results.
	URLs bool
	// Budget caps an outline's characters; 0 means OutlineBudget.
	Budget int
}

// Observe answers an observe request for the scopes desktop, window
// (with find), element and point.
func (o *Observer) Observe(ctx context.Context, req protocol.ObserveRequest) (*protocol.ObserveResult, error) {
	if req.Tail && !req.Text {
		return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "tail needs text: true", Hint: "observe with text: true, tail: true"}
	}
	scope := req.Scope
	if scope == "" {
		scope = protocol.ScopeWindow
		if req.Target != nil {
			scope = protocol.ScopeElement
			if req.Target.Point != nil {
				scope = protocol.ScopePoint
			}
		}
	}
	walk := o.Refs.Begin()
	defer walk.End()
	raw, err := o.Source.Windows(ctx)
	if err != nil {
		return nil, internal(err)
	}
	listed := ListWindows(raw, o.Input)
	refs := walk.Windows(listed)
	res := &protocol.ObserveResult{View: walk.View(), Windows: windowList(listed, refs)}
	switch scope {
	case protocol.ScopeDesktop:
		for i, w := range listed {
			if w.Active {
				win := res.Windows[i]
				res.Window = &win
				break
			}
		}
		return res, nil
	case protocol.ScopeWindow:
		return o.window(ctx, walk, res, listed, req)
	case protocol.ScopeElement:
		return o.element(ctx, walk, res, listed, req)
	case protocol.ScopePoint:
		return o.point(ctx, walk, res, listed, req)
	}
	return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: fmt.Sprintf("scope %q is not desktop, window, element or point", req.Scope)}
}

func windowList(ws []RawWindow, refs []string) []protocol.Window {
	byKey := make(map[string]string, len(ws))
	for i, w := range ws {
		byKey[w.Key] = refs[i]
	}
	out := make([]protocol.Window, len(ws))
	for i, w := range ws {
		out[i] = WindowOf(w, refs[i], byKey[w.OwnerKey])
	}
	return out
}

// Window resolves a window name (a ref or a title) among the listed windows
// and checks that it can be read.
func (o *Observer) Window(listed []RawWindow, want string, pid int) (RawWindow, error) {
	var w RawWindow
	if refShape.MatchString(want) {
		e, err := o.Refs.Lookup(want)
		if err != nil {
			return RawWindow{}, err
		}
		if !e.Window {
			return RawWindow{}, &protocol.Error{Code: protocol.ErrInvalid, Message: want + " is an element, not a window"}
		}
		found := false
		for _, l := range listed {
			if l.Key == e.WindowKey {
				w, found = l, true
				break
			}
		}
		if !found {
			return RawWindow{}, &protocol.Error{
				Code:    protocol.ErrWindowNotFound,
				Message: "window " + want + " is gone",
				Hint:    "windows: " + titles(listed),
			}
		}
	} else {
		var err error
		if w, err = PickWindow(listed, want, pid); err != nil {
			return RawWindow{}, err
		}
	}
	if err := Actionable(w, o.Input); err != nil {
		return RawWindow{}, err
	}
	return w, nil
}

func (o *Observer) window(ctx context.Context, walk *Walk, res *protocol.ObserveResult, listed []RawWindow, req protocol.ObserveRequest) (*protocol.ObserveResult, error) {
	var sel Selector
	if req.Find != "" {
		var err error
		if sel, err = ParseSelector(req.Find); err != nil {
			return nil, err
		}
	}
	w, err := o.Window(listed, req.Window, 0)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	// find always searches the menu bar.
	tree, err := o.Source.Tree(ctx, w, TreeOptions{Menus: req.Menus || req.Find != "", URLs: o.URLs, Web: req.Web})
	if err != nil {
		return nil, internal(err)
	}
	snap := walk.Snapshot(tree)
	win := WindowOf(w, snap.WindowRef, ownerRef(res.Windows, listed, w))
	res.Window = &win
	opts := OutlineOptions{Menus: req.Menus, URLs: o.URLs, Budget: o.Budget}
	if req.Find != "" {
		m, err := Find(snap, sel)
		if err != nil {
			if pe, ok := err.(*protocol.Error); ok && pe.Code == protocol.ErrTargetNotFound {
				pe.Observe = &protocol.ObserveResult{View: res.View, Window: res.Window}
			}
			return nil, err
		}
		opts.Only = m.Index
		res.Resolved = &protocol.Resolution{How: "name", Ms: int(time.Since(start).Milliseconds())}
	}
	out := RenderOutline(snap, opts)
	res.Outline = out.Text
	res.Notes = append(res.Notes, out.Notes...)
	res.Truncated = out.Cut
	if req.Nodes {
		res.Nodes = Nodes(snap, out.Shown, req.Menus, o.URLs)
	}
	return res, nil
}

func ownerRef(ws []protocol.Window, listed []RawWindow, w RawWindow) string {
	if w.OwnerKey == "" {
		return ""
	}
	for i, l := range listed {
		if l.Key == w.OwnerKey {
			return ws[i].Ref
		}
	}
	return ""
}

func (o *Observer) element(ctx context.Context, walk *Walk, res *protocol.ObserveResult, listed []RawWindow, req protocol.ObserveRequest) (*protocol.ObserveResult, error) {
	t := req.Target
	if t == nil || (t.Ref == "" && t.Select == "") {
		return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "scope element needs a target ref or selector"}
	}
	var (
		h   Handle
		ref string
	)
	if t.Ref != "" {
		e, err := o.Refs.Lookup(t.Ref)
		if err != nil {
			return nil, err
		}
		if e.Window {
			return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: t.Ref + " is a window; scope element reads an element", Hint: "use scope window for a window's outline"}
		}
		for _, l := range listed {
			if l.Key == e.WindowKey {
				if err := Actionable(l, o.Input); err != nil {
					return nil, err
				}
			}
		}
		h, ref = e.Handle, e.Ref
	} else {
		sel, err := ParseSelector(t.Select)
		if err != nil {
			return nil, err
		}
		want := t.Window
		if want == "" {
			want = req.Window
		}
		w, err := o.Window(listed, want, 0)
		if err != nil {
			return nil, err
		}
		tree, err := o.Source.Tree(ctx, w, TreeOptions{Menus: true, URLs: true, Web: req.Web})
		if err != nil {
			return nil, internal(err)
		}
		snap := walk.Snapshot(tree)
		i, _, err := Resolve(snap, sel)
		if err != nil {
			return nil, err
		}
		h, ref = tree.Nodes[i].Handle, snap.Refs[i]
	}
	n, cut, err := o.Source.Read(ctx, h, req.Text, req.Tail)
	if err != nil {
		return nil, StaleRef(ref)
	}
	node, notes := ReadNode(n, ref, req.Text, req.Tail, req.Web, cut)
	res.Element = &node
	res.Notes = append(res.Notes, notes...)
	return res, nil
}

// ReadNode is an element read: the whole name, and the value as a preview
// of PreviewChars characters, or with full up to MaxText characters (the
// head, or with tail the end), cut on whole characters and flagged
// truncated only when something was cut. A link carries its target; with web
// the read adds the DOM id and the page address (in URL when the element is
// not a link, and as a note).
func ReadNode(n RawNode, ref string, full, tail, web, cut bool) (protocol.Node, []string) {
	out := protocol.Node{
		Ref:    ref,
		Role:   orDefault(n.Role, "generic"),
		Name:   StripObjects(n.Name),
		States: agentStates(n.States),
		Bounds: n.Bounds,
		Depth:  n.Depth,
		URL:    n.URL,
	}
	var notes []string
	if n.Value != nil {
		v := *n.Value
		limit := PreviewChars
		if full {
			limit = MaxText
		}
		total := utf8.RuneCountInString(v)
		if total > limit {
			v = Clip(v, limit, full && tail)
			cut = true
		}
		out.Value = &v
		switch {
		case full:
			out.Truncated = cut
		case cut:
			more := ""
			if total > limit {
				more = " of " + strconv.Itoa(total)
			}
			notes = append(notes, fmt.Sprintf("value preview: the first %d characters%s; observe with text: true for the whole text, and tail: true for its end", PreviewChars, more))
		}
	}
	if web {
		out.DOMID = n.DOMID
		if n.Page != "" {
			if out.URL == "" {
				out.URL = n.Page
			}
			notes = append(notes, "page: "+n.Page)
		}
	}
	return out, notes
}

func (o *Observer) point(ctx context.Context, walk *Walk, res *protocol.ObserveResult, listed []RawWindow, req protocol.ObserveRequest) (*protocol.ObserveResult, error) {
	p := req.Target.Point
	if p.Space != "" && p.Space != protocol.SpaceScreen {
		return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "scope point takes a screen point", Hint: "convert window or image points to screen coordinates first"}
	}
	x, y := int(p.X), int(p.Y)
	node, path, win, covered, err := o.Source.At(ctx, x, y)
	if err != nil {
		return nil, internal(err)
	}
	if req.Window != "" {
		// The asked window's element answers, and a window of another
		// app on top there is named as covering it.
		asked, err := o.Window(listed, req.Window, 0)
		if err != nil {
			return nil, err
		}
		if in, ok := o.Source.(PointInWindow); ok && (win == nil || win.PID != asked.PID) {
			n, pth, w, cov, err := in.AtIn(ctx, asked, x, y)
			if err == nil && w != nil && w.PID == asked.PID {
				node, path, win, covered = n, pth, w, cov
			}
		}
	}
	if win == nil {
		w, err := WindowAt(listed, x, y)
		if err != nil {
			return nil, err
		}
		win = &w
	}
	if err := Actionable(*win, o.Input); err != nil {
		return nil, err
	}
	res.At = HitOf(walk, *win, node, path, covered, res.Windows, listed)
	res.Window = res.At.Window
	return res, nil
}

// HitOf assembles a point answer: the element, its ancestors (innermost
// first), its window and the window covering the point, if any.
func HitOf(walk *Walk, win RawWindow, node *RawNode, path []RawNode, covered *RawWindow, ws []protocol.Window, listed []RawWindow) *protocol.Hit {
	wref := walk.Window(win)
	w := WindowOf(win, wref, ownerRef(ws, listed, win))
	hit := &protocol.Hit{Window: &w}
	if node != nil {
		n := NodeOf(*node, walk.Element(wref, win.Key, *node), "", true)
		hit.Element = &n
	}
	for _, a := range path {
		hit.Path = append(hit.Path, NodeOf(a, walk.Element(wref, win.Key, a), "", true))
	}
	if covered != nil {
		c := WindowOf(*covered, walk.Window(*covered), "")
		hit.Covered = &c
	}
	return hit
}

func internal(err error) *protocol.Error {
	if pe, ok := err.(*protocol.Error); ok {
		return pe
	}
	return &protocol.Error{Code: protocol.ErrInternal, Message: err.Error()}
}
