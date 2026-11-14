//go:build darwin

package desk

import (
	"context"
	"errors"
	"path/filepath"

	"fennec/cua/internal/ax"
	"fennec/cua/internal/view"
)

// At reads the element at a desktop point, its ancestors innermost first,
// its window, and the window on top at the point when that is another one.
func (d *Desk) At(ctx context.Context, x, y int) (*view.RawNode, []view.RawNode, *view.RawWindow, *view.RawWindow, error) {
	scale := ax.BackingScale()
	px, py := float64(x)/scale, float64(y)/scale
	el, err := ax.ElementAt(px, py)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return d.hitFrom(el, px, py, scale)
}

// AtIn reads the element of window w's app at a desktop point, even where
// another app's window lies on top, which it then names as covering.
func (d *Desk) AtIn(ctx context.Context, w view.RawWindow, x, y int) (*view.RawNode, []view.RawNode, *view.RawWindow, *view.RawWindow, error) {
	scale := ax.BackingScale()
	px, py := float64(x)/scale, float64(y)/scale
	app := ax.App(w.PID)
	defer app.Release()
	el, err := app.ElementAtIn(px, py)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return d.hitFrom(el, px, py, scale)
}

// hitFrom reads a hit element, its ancestors up to its window, and the
// window on top at the point when that is another one.
func (d *Desk) hitFrom(el *ax.Element, px, py, scale float64) (*view.RawNode, []view.RawNode, *view.RawWindow, *view.RawWindow, error) {
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
			// The app's name decides policy on the window (security
			// prompts, the allowlist).
			w.App = appName(pid)
			if id, ok := parent.WindowID(); ok {
				w.Key = ax.WindowKey(id)
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
	// App windows cover another, floating panels and modal panels
	// included (layers below the Dock's); system chrome on the Dock's layer
	// and above (Dock, menu bar, Notification Center) never does.
	apps := server[:0:0]
	for _, w := range server {
		if w.Layer >= 0 && w.Layer < dockLayer {
			apps = append(apps, w)
		}
	}
	w, ok := ax.TopWindowAt(apps, px, py, d.excluded())
	if !ok {
		return nil
	}
	if win != nil {
		if id, ok := serverID(win.Key); ok && id == w.ID {
			return nil
		}
		if win.Key == "" && w.PID == win.PID {
			return nil
		}
	}
	return &view.RawWindow{Key: ax.WindowKey(w.ID), PID: w.PID, App: w.Owner, Title: w.Title}
}

// Focused returns the element with the keyboard focus in an app. When the
// app's focus attribute yields nothing in its focused window, one tree read
// of that window finds the first focused node.
func (d *Desk) Focused(ctx context.Context, pid int) (*view.RawNode, error) {
	app := ax.App(pid)
	defer app.Release()
	app.SetTimeout(MessagingTimeout.Seconds())
	scale := ax.BackingScale()
	var win *ax.Element
	if v, err := app.Attr("AXFocusedWindow"); err == nil {
		win, _ = v.(*ax.Element)
	}
	n, ok, err := ax.FocusedElement(ax.FocusSource[view.RawNode]{
		Direct: func() (view.RawNode, bool) {
			v, err := app.Attr("AXFocusedUIElement")
			if err != nil {
				return view.RawNode{}, false
			}
			el, ok := v.(*ax.Element)
			if !ok {
				return view.RawNode{}, false
			}
			attrs, err := el.Attrs(nodeAttrs)
			if err != nil {
				return view.RawNode{}, false
			}
			return node(el, attrs, scale, view.TreeOptions{Web: true}), true
		},
		InWindow: func(n view.RawNode) bool {
			el, _ := n.Handle.(*ax.Element)
			if win == nil || el == nil {
				return true // nothing to match against: trust the app
			}
			w, err := el.Attr("AXWindow")
			if err != nil {
				return true // no window attribute (a menu): trust the app
			}
			we, ok := w.(*ax.Element)
			return !ok || we.Equal(win)
		},
		Tree: func() ([]view.RawNode, error) {
			if win == nil {
				return nil, errors.New("no focused window")
			}
			t, err := d.Tree(ctx, view.RawWindow{Handle: win, PID: pid}, view.TreeOptions{Web: true})
			return t.Nodes, err
		},
		Focused: func(n view.RawNode) bool { return n.HasState("focused") },
	})
	if err != nil || !ok {
		return nil, err
	}
	return &n, nil
}

var _ view.Source = (*Desk)(nil)

// dockLayer is the Dock's window server layer (kCGDockWindowLevel); app,
// floating and modal panel windows sit below it.
const dockLayer = ax.DockLayer

// appName is the name of the app with pid, as its accessibility element
// gives it.
func appName(pid int) string {
	app := ax.App(pid)
	defer app.Release()
	v, err := app.Attr("AXTitle")
	if err != nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// FrontWindow names what is in front when no listed window is: the front
// process's topmost window, such as a system prompt or a window of a
// process that lists no accessibility windows. ok is false when nothing
// can be named.
func (d *Desk) FrontWindow(ctx context.Context) (view.RawWindow, bool) {
	pid := ax.FrontmostPID()
	if pid == 0 {
		pid = focusedPID()
	}
	server, err := ax.ServerWindows(true)
	if err != nil {
		return view.RawWindow{}, false
	}
	if pid == 0 {
		return view.RawWindow{}, false
	}
	// Its topmost window below the menu bar (prompts sit on the modal
	// panel layer, above the Dock).
	var front *ax.ServerWindow
	for i, w := range server {
		if w.PID == pid && w.Layer >= 0 && w.Layer < menuBarLayer && w.Alpha > 0 {
			front = &server[i]
			break
		}
	}
	if front == nil {
		p, err := ax.ProcessPath(pid)
		if err != nil {
			return view.RawWindow{}, false
		}
		return view.RawWindow{PID: pid, App: filepath.Base(p)}, true
	}
	return view.RawWindow{Key: ax.WindowKey(front.ID), PID: front.PID, App: front.Owner, Title: front.Title}, true
}

// menuBarLayer is the menu bar's window server layer (kCGMainMenuWindowLevel).
const menuBarLayer = 24
