//go:build darwin

package desk

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"fennec/cua/internal/ax"
	"fennec/cua/internal/view"
)

// Attributes read for every element in one batched call.
var nodeAttrs = []string{
	"AXRole", "AXSubrole", "AXRoleDescription", "AXTitle", "AXDescription", "AXValue",
	"AXFocused", "AXSelected", "AXExpanded", "AXEnabled", "AXPosition", "AXSize",
	"AXChildren", "AXHelp", "AXPlaceholderValue", "AXURL", "AXDOMIdentifier",
	"AXMenuItemCmdChar", "AXMenuItemCmdModifiers",
}

const (
	aRole = iota
	aSubrole
	aRoleDesc
	aTitle
	aDesc
	aValue
	aFocused
	aSelected
	aExpanded
	aEnabled
	aPosition
	aSize
	aChildren
	aHelp
	aPlaceholder
	aURL
	aDOMID
	aCmdChar
	aCmdMods
)

// ErrStale is a read aimed at a window whose app is not answering.
var ErrStale = errors.New("the app is busy or hung and does not answer accessibility; try again shortly")

// Tree walks a window's elements in pre-order, up to the node and depth caps.
func (d *Desk) Tree(ctx context.Context, w view.RawWindow, opts view.TreeOptions) (view.Tree, error) {
	if w.Stale {
		return view.Tree{}, ErrStale
	}
	root, ok := w.Handle.(*ax.Element)
	if !ok || root == nil {
		return view.Tree{}, fmt.Errorf("window %q has no accessibility element", w.Title)
	}
	scale := ax.BackingScale()
	t := view.Tree{Window: w}
	type item struct {
		el     *ax.Element
		parent int
		depth  int
	}
	stack := []item{{el: root, parent: -1}}
	if opts.Menus && w.PID != 0 {
		app := ax.App(w.PID)
		app.SetTimeout(MessagingTimeout.Seconds())
		if mb, err := app.Attr("AXMenuBar"); err == nil {
			if el, ok := mb.(*ax.Element); ok {
				// The menu bar is walked after the window, as a second root.
				stack = append([]item{{el: el, parent: -1}}, stack...)
			}
		}
		app.Release()
	}
	for len(stack) > 0 {
		if ctx.Err() != nil {
			return t, ctx.Err()
		}
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if len(t.Nodes) >= view.MaxNodes {
			t.Truncated = true
			break
		}
		v, err := it.el.Attrs(nodeAttrs)
		if err != nil {
			if it.parent == -1 {
				return t, err
			}
			continue
		}
		n := node(it.el, v, scale, opts)
		n.Parent, n.Depth = it.parent, it.depth
		idx := len(t.Nodes)
		t.Nodes = append(t.Nodes, n)
		kids, _ := v[aChildren].([]any)
		if it.depth+1 > view.MaxDepth {
			if len(kids) > 0 {
				t.Truncated = true
			}
			continue
		}
		for i := len(kids) - 1; i >= 0; i-- {
			if el, ok := kids[i].(*ax.Element); ok {
				stack = append(stack, item{el: el, parent: idx, depth: it.depth + 1})
			}
		}
	}
	return t, nil
}

// node converts one element's attributes.
func node(el *ax.Element, v []any, scale float64, opts view.TreeOptions) view.RawNode {
	role, sub := str(v[aRole]), str(v[aSubrole])
	n := view.RawNode{
		Handle: el,
		Key:    strconv.FormatUint(el.Hash(), 16),
		Role:   view.NormalizeRole(role, sub, str(v[aRoleDesc])),
		Name:   str(v[aTitle]),
		Desc:   str(v[aDesc]),
	}
	if n.Name == "" {
		n.Name, n.Desc = n.Desc, ""
	}
	if n.Desc == "" {
		n.Desc = firstNonEmpty(str(v[aHelp]), str(v[aPlaceholder]))
	}
	if val, ok := valueString(v[aValue]); ok {
		n.Value = &val
	}
	n.States = view.States(role, sub, v[aValue], boolean(v[aFocused]), boolean(v[aSelected]), boolean(v[aExpanded]), enabled(v[aEnabled]))
	if r, ok := frame(v[aPosition], v[aSize]); ok {
		n.Bounds = toDesk(r, scale)
		// A menu item has a frame only while its menu is open: then it is
		// showing, and a closed menu's items stay hidden from matching.
		if role == "AXMenuItem" && r.W > 0 && r.H > 0 && !n.HasState("showing") {
			n.States = append(n.States, "showing")
		}
	}
	if u, ok := v[aURL].(ax.URL); ok && (opts.URLs || n.Role == "link") {
		n.URL = string(u)
	}
	if opts.Web {
		n.DOMID = str(v[aDOMID])
	}
	n.Keys = shortcut(str(v[aCmdChar]), v[aCmdMods])
	return n
}

func enabled(v any) bool {
	b, ok := v.(bool)
	return !ok || b // unknown counts as enabled
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// valueString renders AXValue: strings as is, numbers without a trailing
// .0, booleans as 1/0. Other kinds have no text value.
func valueString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		if x {
			return "1", true
		}
		return "0", true
	case ax.URL:
		return string(x), true
	}
	return "", false
}

// shortcut renders a menu item's key equivalent; see ax.MenuShortcut.
func shortcut(char string, mods any) string { return ax.MenuShortcut(char, mods) }

// Read reads one element; full returns up to MaxText characters of its value
// (from the end with tail) and reports whether it was cut.
func (d *Desk) Read(ctx context.Context, h view.Handle, full, tail bool) (view.RawNode, bool, error) {
	el, ok := h.(*ax.Element)
	if !ok || el == nil {
		return view.RawNode{}, false, errors.New("not an element")
	}
	v, err := el.Attrs(nodeAttrs)
	if err != nil {
		return view.RawNode{}, false, err
	}
	n := node(el, v, ax.BackingScale(), view.TreeOptions{URLs: true, Web: true})
	cut := false
	if n.Value != nil {
		s := *n.Value
		limit := view.PreviewChars
		if full {
			limit = view.MaxText
		}
		if utf8.RuneCountInString(s) > limit {
			r := []rune(s)
			if tail {
				s = string(r[len(r)-limit:])
			} else {
				s = string(r[:limit])
			}
			cut = true
		}
		n.Value = &s
	}
	return n, cut, nil
}
