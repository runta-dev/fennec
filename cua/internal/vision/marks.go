package vision

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// Vision lists.
const (
	// MaxElements is the most elements a vision observe numbers.
	MaxElements = 300
	// ListChars is the element list's text budget.
	ListChars = 15000
	// MaxIndent caps the depth an element line is indented by.
	MaxIndent = 10
	// MinWindowPoints leaves out windows narrower or shorter than this.
	MinWindowPoints = 10
)

// ListCut ends an element list cut at ListChars.
const ListCut = "[element list cut; observe with find to narrow it]"

// WindowBox is one on-screen window for the vision window lines.
type WindowBox struct {
	Ref    string
	PID    int
	ID     uint32
	App    string
	Title  string
	Bounds [4]float64 // desktop coordinates
	Z      int        // stacking order, higher is nearer the front
}

// ParseWindowList reads a driver list_windows reply. Bounds may be whole or
// fractional numbers (fractions rounded) and come in points; backing turns
// them into desktop coordinates. The list is ordered front to back.
func ParseWindowList(raw json.RawMessage, backing float64) ([]WindowBox, error) {
	var r toolResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	var s struct {
		Windows []struct {
			PID    int    `json:"pid"`
			ID     uint32 `json:"window_id"`
			App    string `json:"app_name"`
			Title  string `json:"title"`
			Z      *int   `json:"z_index"`
			Bounds map[string]float64
		} `json:"windows"`
	}
	if err := json.Unmarshal(r.Structured, &s); err != nil {
		return nil, err
	}
	if !Positive(backing) {
		backing = 1
	}
	out := make([]WindowBox, 0, len(s.Windows))
	for _, w := range s.Windows {
		get := func(keys ...string) float64 {
			for _, k := range keys {
				if v, ok := w.Bounds[k]; ok {
					return math.Round(v)
				}
			}
			return 0
		}
		b := WindowBox{PID: w.PID, ID: w.ID, App: w.App, Title: w.Title, Z: math.MinInt}
		if w.Z != nil {
			b.Z = *w.Z
		}
		b.Bounds = [4]float64{get("x") * backing, get("y") * backing, get("width", "w") * backing, get("height", "h") * backing}
		out = append(out, b)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Z > out[j].Z })
	return out, nil
}

// WindowsOf turns a view's window list (front to back) into window boxes;
// id resolves a window ref to its window server id.
func WindowsOf(ws []protocol.Window, id func(ref string) (uint32, bool)) []WindowBox {
	out := make([]WindowBox, 0, len(ws))
	for i, w := range ws {
		if w.Bounds == nil || w.Space == "other" || w.Hidden != "" {
			continue
		}
		b := WindowBox{Ref: w.Ref, PID: w.PID, App: w.App, Title: w.Title, Z: len(ws) - i}
		if id != nil {
			if n, ok := id(w.Ref); ok {
				b.ID = n
			}
		}
		for k := range 4 {
			b.Bounds[k] = float64(w.Bounds[k])
		}
		out = append(out, b)
	}
	return out
}

// WindowLines lists the windows front to back, one line each with pid,
// window id, app, quoted title and origin and size in screenshot pixels.
// Windows narrower or shorter than MinWindowPoints points, or wholly left
// of or above the screen, are left out.
func WindowLines(ws []WindowBox, shot Shot, backing float64) string {
	if !Positive(backing) {
		backing = 1
	}
	var b strings.Builder
	b.WriteString("Windows (frontmost first):\n")
	for _, w := range ws {
		x, y, ww, hh := w.Bounds[0], w.Bounds[1], w.Bounds[2], w.Bounds[3]
		if ww/backing < MinWindowPoints || hh/backing < MinWindowPoints || x+ww <= 0 || y+hh <= 0 {
			continue
		}
		ix, iy := shot.FromDesktop(x, y)
		b.WriteString("- ")
		if w.Ref != "" {
			b.WriteString(w.Ref + " ")
		}
		q, _ := view.Quote(view.StripObjects(w.App), view.NameChars)
		fmt.Fprintf(&b, "pid=%d window_id=%d %s %s at (%.0f,%.0f) %.0fx%.0f\n",
			w.PID, w.ID, q, view.Field(w.Title, view.NameChars), ix, iy, ww/shot.Scale, hh/shot.Scale)
	}
	return b.String()
}

// Element is one numbered element of a vision observe.
type Element struct {
	N        int
	Ref      string
	Role     string
	Name     string // as read; placeholders are removed only when shown
	Value    *string
	Disabled bool
	Depth    int
	Center   *[2]float64 // desktop coordinates
}

// ElementsOf numbers a window's nodes in walk order, at most MaxElements.
func ElementsOf(nodes []protocol.Node) []Element {
	out := make([]Element, 0, min(len(nodes), MaxElements))
	for _, n := range nodes {
		if len(out) == MaxElements {
			break
		}
		e := Element{N: len(out) + 1, Ref: n.Ref, Role: n.Role, Name: n.Name, Value: n.Value, Depth: n.Depth}
		for _, s := range n.States {
			if s == "disabled" {
				e.Disabled = true
			}
		}
		if b := n.Bounds; b != nil && b[2] > 0 && b[3] > 0 {
			e.Center = &[2]float64{float64(b[0]) + float64(b[2])/2, float64(b[1]) + float64(b[3])/2}
		}
		out = append(out, e)
	}
	return out
}

// ElementList renders numbered elements, one per line, indented by depth
// (at most MaxIndent levels): number, role, quoted label, a value preview,
// the centre in screenshot pixels and a disabled flag. Labels and values
// are escaped so text in an app cannot forge another line. Past ListChars
// the list stops with a note; the marks still cover every element.
func ElementList(els []Element, shot Shot) (string, []protocol.Mark) {
	var b strings.Builder
	marks := make([]protocol.Mark, 0, len(els))
	cut := false
	for _, e := range els {
		m := protocol.Mark{N: e.N, Ref: e.Ref, Role: e.Role, Name: view.StripObjects(e.Name)}
		line := strings.Repeat("  ", min(e.Depth, MaxIndent)) + view.VisionLine(e.N, e.Role, e.Name, e.Value)
		if e.Center != nil {
			x, y := shot.FromDesktop(e.Center[0], e.Center[1])
			m.Center = [2]float64{math.Round(x), math.Round(y)}
			line += fmt.Sprintf(" center=(%.0f,%.0f)", m.Center[0], m.Center[1])
		}
		if e.Disabled {
			line += " disabled"
		}
		marks = append(marks, m)
		if cut {
			continue
		}
		if b.Len()+len(line)+1 > ListChars {
			cut = true
			continue
		}
		b.WriteString(line + "\n")
	}
	if cut {
		b.WriteString(ListCut + "\n")
	}
	return b.String(), marks
}

// PickVisionWindow is the window a vision observe numbers: the given ref,
// else the frontmost listed window of pid, else the frontmost application
// window. ws is front to back.
func PickVisionWindow(ws []protocol.Window, pid int, ref string) (protocol.Window, bool) {
	for _, w := range ws {
		switch {
		case ref != "":
			if w.Ref == ref {
				return w, true
			}
		case pid != 0:
			if w.PID == pid {
				return w, true
			}
		case w.Role != "sheet":
			return w, true
		}
	}
	return protocol.Window{}, false
}
