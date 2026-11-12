//go:build darwin && cgo

package record

import (
	"strconv"

	"fennec/cua/internal/ax"
	"fennec/cua/internal/view"
)

// readTimeout bounds each accessibility read the tap makes, so a hung app
// cannot stall the recording.
const readTimeout = 0.25

var hitAttrs = []string{"AXRole", "AXSubrole", "AXRoleDescription", "AXTitle", "AXDescription", "AXHelp", "AXDOMIdentifier", "AXURL"}

const (
	hRole = iota
	hSubrole
	hRoleDesc
	hTitle
	hDesc
	hHelp
	hDOMID
	hURL
)

// axReader reads the tap's context from the accessibility tree.
type axReader struct{}

type axNode struct {
	el      *ax.Element
	axRole  string
	subrole string
	role    string
	name    string
	desc    string
	domID   string
	url     string
}

func readNode(el *ax.Element) (axNode, bool) {
	v, err := el.Attrs(hitAttrs)
	if err != nil {
		return axNode{}, false
	}
	s := func(i int) string { x, _ := v[i].(string); return x }
	n := axNode{el: el, axRole: s(hRole), subrole: s(hSubrole), name: s(hTitle), desc: s(hDesc), domID: s(hDOMID)}
	n.role = view.NormalizeRole(n.axRole, n.subrole, s(hRoleDesc))
	if n.name == "" {
		n.name, n.desc = n.desc, ""
	}
	if n.desc == "" {
		n.desc = s(hHelp)
	}
	if u, ok := v[hURL].(ax.URL); ok {
		n.url = string(u)
	}
	return n, true
}

func parentOf(el *ax.Element) *ax.Element {
	p, err := el.Attr("AXParent")
	if err != nil {
		return nil
	}
	e, _ := p.(*ax.Element)
	return e
}

// ancestry walks up from an element: its ancestors to the window
// (innermost first), the window, and the web page holding it.
func ancestry(el *ax.Element) (path []PathNode, win *PathNode, page string) {
	cur := el
	for i := 0; i < view.MaxDepth; i++ {
		p := parentOf(cur)
		if p == nil {
			break
		}
		n, ok := readNode(p)
		if !ok || n.axRole == "AXApplication" {
			break
		}
		if n.axRole == "AXWebArea" && page == "" {
			page = n.url
		}
		node := PathNode{Role: n.role, Name: n.name}
		if n.axRole == "AXWindow" || n.axRole == "AXSheet" {
			if id, ok := p.WindowID(); ok {
				node.Key = "cg:" + strconv.FormatUint(uint64(id), 10)
			}
			path = append(path, node)
			w := node
			return path, &w, page
		}
		path = append(path, node)
		cur = p
	}
	// Deeper than the walk reaches: the element still names its window.
	if v, err := el.Attr("AXWindow"); err == nil {
		if w, ok := v.(*ax.Element); ok {
			if n, ok := readNode(w); ok {
				node := PathNode{Role: n.role, Name: n.name}
				if id, ok := w.WindowID(); ok {
					node.Key = "cg:" + strconv.FormatUint(uint64(id), 10)
				}
				win = &node
			}
		}
	}
	return path, win, page
}

func (axReader) HitAt(x, y float64) *Hit {
	scale := ax.BackingScale()
	el, err := ax.ElementAt(x/scale, y/scale)
	if err != nil || el == nil {
		return nil
	}
	el.SetTimeout(readTimeout)
	n, ok := readNode(el)
	if !ok {
		return nil
	}
	pid, _ := el.PID()
	h := &Hit{Role: n.role, Name: n.name, Desc: n.desc, PID: pid, Secret: n.subrole == "AXSecureTextField"}
	path, win, page := ancestry(el)
	h.Path, h.Window = path, win
	if page == "" && n.axRole == "AXWebArea" {
		page = n.url
	}
	if page != "" {
		h.Web = &Web{URL: page, ID: n.domID}
	}
	return h
}

func focusedApp() *ax.Element {
	sys := ax.SystemWide()
	defer sys.Release()
	sys.SetTimeout(readTimeout)
	v, err := sys.Attr("AXFocusedApplication")
	if err != nil {
		return nil
	}
	app, _ := v.(*ax.Element)
	return app
}

func focusedElement() *ax.Element {
	app := focusedApp()
	if app == nil {
		return nil
	}
	app.SetTimeout(readTimeout)
	v, err := app.Attr("AXFocusedUIElement")
	if err != nil {
		return nil
	}
	el, _ := v.(*ax.Element)
	return el
}

func (axReader) Typing() (int, bool) {
	app := focusedApp()
	if app == nil {
		return 0, secureInputOn()
	}
	pid, _ := app.PID()
	secure := secureInputOn()
	if v, err := app.Attr("AXFocusedUIElement"); err == nil {
		if el, ok := v.(*ax.Element); ok {
			if sub, err := el.Attr("AXSubrole"); err == nil && sub == "AXSecureTextField" {
				secure = true
			}
		}
	}
	return pid, secure
}

func (axReader) Focused(secure bool) *Field {
	el := focusedElement()
	if el == nil {
		return nil
	}
	el.SetTimeout(readTimeout)
	n, ok := readNode(el)
	if !ok {
		return nil
	}
	f := &Field{Role: n.role, Name: n.name, Secure: secure || n.subrole == "AXSecureTextField", ID: n.domID}
	_, win, page := ancestry(el)
	if win != nil {
		f.Frame, f.Window = win.Role, win.Name
	}
	f.URL = page
	return f
}
