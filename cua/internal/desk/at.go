//go:build darwin

package desk

import (
	"context"
	"errors"
	"strconv"

	"fennec/cua/internal/ax"
	"fennec/cua/internal/view"
)

// Window server layers at or above this are system chrome (menu bar, Dock,
// overlays) rather than app windows.
const chromeLayer = 102

// At reads the element at a desktop point, its ancestors innermost first,
// its window, and the window on top at the point when that is another one.
func (d *Desk) At(ctx context.Context, x, y int) (*view.RawNode, []view.RawNode, *view.RawWindow, *view.RawWindow, error) {
	scale := ax.BackingScale()
	px, py := float64(x)/scale, float64(y)/scale
	el, err := ax.ElementAt(px, py)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	v, err := el.Attrs(nodeAttrs)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	opts := view.TreeOptions{URLs: true}
	n := node(el, v, scale, opts)
	var path []view.RawNode
	var win *view.RawWindow
	cur := el
	for i := 0; i < view.MaxDepth; i++ {
		p, err := cur.Attr("AXParent")
		if err != nil {
			break
		}
		parent, ok := p.(*ax.Element)
		if !ok {
			break
		}
		pv, err := parent.Attrs(nodeAttrs)
		if err != nil {
			break
		}
		pn := node(parent, pv, scale, opts)
		role := str(pv[aRole])
		if role == "AXApplication" {
			break
		}
		path = append(path, pn)
		if role == "AXWindow" || role == "AXSheet" {
			pid, _ := parent.PID()
			w := view.RawWindow{Handle: parent, PID: pid, Title: pn.Name, Role: view.WindowRole(str(pv[aSubrole]), role == "AXSheet")}
			if id, ok := parent.WindowID(); ok {
				w.Key = "cg:" + strconv.FormatUint(uint64(id), 10)
			}
			win = &w
			break
		}
		cur = parent
	}
	covered := d.coverAt(px, py, win)
	return &n, path, win, covered, nil
}

// coverAt names the topmost app window at a point (in points) when it is not
// win.
func (d *Desk) coverAt(px, py float64, win *view.RawWindow) *view.RawWindow {
	server, err := ax.ServerWindows(true)
	if err != nil {
		return nil
	}
	for _, w := range server {
		if w.Layer >= chromeLayer || w.Alpha <= 0 || w.Layer != 0 {
			continue
		}
		b := w.Bounds
		if px < b.X || py < b.Y || px >= b.X+b.W || py >= b.Y+b.H {
			continue
		}
		if win != nil {
			if id, ok := serverID(win.Key); ok && id == w.ID {
				return nil
			}
			if win.Key == "" && w.PID == win.PID {
				return nil
			}
		}
		return &view.RawWindow{Key: "cg:" + strconv.FormatUint(uint64(w.ID), 10), PID: w.PID, App: w.Owner, Title: w.Title}
	}
	return nil
}

// Focused returns the element with the keyboard focus in an app. When the
// app's focus attribute yields nothing, one tree read of its focused window
// finds the first focused node.
func (d *Desk) Focused(ctx context.Context, pid int) (*view.RawNode, error) {
	app := ax.App(pid)
	defer app.Release()
	app.SetTimeout(MessagingTimeout.Seconds())
	scale := ax.BackingScale()
	if v, err := app.Attr("AXFocusedUIElement"); err == nil {
		if el, ok := v.(*ax.Element); ok {
			if attrs, err := el.Attrs(nodeAttrs); err == nil {
				n := node(el, attrs, scale, view.TreeOptions{Web: true})
				return &n, nil
			}
		}
	}
	v, err := app.Attr("AXFocusedWindow")
	if err != nil {
		return nil, err
	}
	win, ok := v.(*ax.Element)
	if !ok {
		return nil, errors.New("no focused window")
	}
	t, err := d.Tree(ctx, view.RawWindow{Handle: win, PID: pid}, view.TreeOptions{Web: true})
	if err != nil {
		return nil, err
	}
	for i := range t.Nodes {
		for _, s := range t.Nodes[i].States {
			if s == "focused" {
				return &t.Nodes[i], nil
			}
		}
	}
	return nil, nil
}

var _ view.Source = (*Desk)(nil)
