package view

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"fennec/cua/internal/protocol"
)

// Snapshot is a window's tree with the refs one walk handed out.
type Snapshot struct {
	Window    RawWindow
	WindowRef string
	Tree      Tree
	// Refs holds each node's ref; a root node that is the window itself
	// carries the window ref.
	Refs []string
	// RootIsWindow is set when Tree.Nodes[0] is the window element.
	RootIsWindow bool
}

// Snapshot hands out refs for a window's tree.
func (w *Walk) Snapshot(tree Tree) Snapshot {
	s := Snapshot{Window: tree.Window, Tree: tree, Refs: make([]string, len(tree.Nodes))}
	s.WindowRef = w.Window(tree.Window)
	s.RootIsWindow = len(tree.Nodes) > 0 && tree.Nodes[0].Parent < 0 && windowRoles[tree.Nodes[0].Role]
	for i, n := range tree.Nodes {
		if i == 0 && s.RootIsWindow {
			s.Refs[i] = s.WindowRef
			continue
		}
		s.Refs[i] = w.Element(s.WindowRef, tree.Window.Key, n)
	}
	return s
}

// parentRef is the ref of node i's parent ("" for the root).
func (s Snapshot) parentRef(i int) string {
	p := s.Tree.Nodes[i].Parent
	if p < 0 || p >= len(s.Refs) {
		return ""
	}
	return s.Refs[p]
}

// OutlineOptions shape an outline.
type OutlineOptions struct {
	Menus bool // include the menu bar
	URLs  bool // show link targets
	// Budget caps the outline's characters; 0 means OutlineBudget.
	Budget int
	// Only, when set, limits the outline to these node indexes (a find).
	Only []int
}

// Outline is the agent's text view of a window.
type Outline struct {
	Text  string
	Notes []string
	// Cut is set when the node cap, the depth cap or the budget shortened it.
	Cut bool
	// Shown lists the node indexes the text holds, in order.
	Shown []int
}

// Notes on a shortened outline.
const (
	noteNodeCap = "outline cut: the window has more than 2500 elements or nests deeper than 64 levels; observe a narrower view with find or scope element"
	noteBudget  = "outline cut at the %d-character budget after %d of %d elements; observe a narrower view with find or scope element"
)

// RenderOutline renders a snapshot: the window line first, then one line
// per element, indented by depth. The menu bar shows only when asked.
func RenderOutline(s Snapshot, opts OutlineOptions) Outline {
	budget := opts.Budget
	if budget <= 0 {
		budget = OutlineBudget
	}
	nodes := s.Tree.Nodes
	var keep []int
	if opts.Only != nil {
		keep = opts.Only
	} else {
		skip := make([]bool, len(nodes))
		for i, n := range nodes {
			if n.Parent >= 0 && n.Parent < i && skip[n.Parent] {
				skip[i] = true
				continue
			}
			if n.Role == "menubar" && !opts.Menus {
				skip[i] = true
				continue
			}
			if i == 0 && s.RootIsWindow {
				continue
			}
			keep = append(keep, i)
		}
	}
	base := 1
	if s.RootIsWindow {
		base = 0
	}
	var b strings.Builder
	used := 0
	add := func(line string) bool {
		n := utf8.RuneCountInString(line)
		if used > 0 {
			n++ // the newline before it
		}
		if used+n > budget {
			return false
		}
		if used > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		used += n
		return true
	}
	out := Outline{}
	add(WindowLine(s.WindowRef, s.Window))
	for k, i := range keep {
		depth := nodes[i].Depth + base
		if opts.Only != nil {
			depth = 1
		}
		if !add(strings.Repeat("  ", depth) + NodeLine(s.Refs[i], nodes[i], opts.URLs)) {
			out.Cut = true
			out.Notes = append(out.Notes, fmt.Sprintf(noteBudget, budget, k, len(keep)))
			break
		}
		out.Shown = append(out.Shown, i)
	}
	if s.Tree.Truncated {
		out.Cut = true
		out.Notes = append([]string{noteNodeCap}, out.Notes...)
	}
	out.Text = b.String()
	return out
}

// WindowLine renders a window: `w3 window "Untitled" (TextEdit) [active]`.
func WindowLine(ref string, w RawWindow) string {
	var b strings.Builder
	b.WriteString(ref)
	b.WriteByte(' ')
	b.WriteString(orDefault(w.Role, "window"))
	b.WriteByte(' ')
	b.WriteString(Field(w.Title, NameChars))
	if w.App != "" {
		b.WriteString(" (")
		q, _ := Quote(StripObjects(w.App), NameChars)
		b.WriteString(q)
		b.WriteByte(')')
	}
	var states []string
	if w.Active {
		states = append(states, "active")
	}
	if w.Hidden != "" {
		states = append(states, w.Hidden)
	}
	if w.Space == "other" {
		states = append(states, "other-space")
	}
	if w.Stale {
		states = append(states, "stale")
	}
	if len(states) > 0 {
		b.WriteString(" [" + strings.Join(states, " ") + "]")
	}
	return b.String()
}

// NodeLine renders an element:
// `e2 checkbox "Wrap" [checked disabled] key=cmd+w url="…": "value"`.
// Names and values are quoted and escaped; a value shows its first
// PreviewChars characters, and a cut one ends in an ellipsis with its length.
func NodeLine(ref string, n RawNode, urls bool) string {
	var b strings.Builder
	b.WriteString(ref)
	b.WriteByte(' ')
	b.WriteString(orDefault(n.Role, "generic"))
	if name := StripObjects(n.Name); name != "" {
		b.WriteByte(' ')
		b.WriteString(Field(name, NameChars))
	}
	if st := agentStates(n.States); len(st) > 0 {
		b.WriteString(" [" + strings.Join(st, " ") + "]")
	}
	if n.Keys != "" {
		q, _ := Quote(n.Keys, NameChars)
		b.WriteString(" key=" + q)
	}
	if urls && n.URL != "" {
		b.WriteString(" url=" + Field(n.URL, NameChars*4))
	}
	if n.Value != nil {
		v := StripObjects(*n.Value)
		q, cut := Quote(v, PreviewChars)
		b.WriteString(`: "` + q + `"`)
		if cut {
			b.WriteString("… (" + strconv.Itoa(utf8.RuneCountInString(v)) + " chars)")
		}
	}
	return b.String()
}

// agentStates drops the states the agent does not need to see.
func agentStates(states []string) []string {
	var out []string
	for _, s := range states {
		if s == "showing" || s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// NodeOf is a structured node: empty fields omitted, names and values without
// embedded-object markers, the value cut to its preview (Truncated set when
// cut), and the link target only when urls is set.
func NodeOf(n RawNode, ref, parent string, urls bool) protocol.Node {
	out := protocol.Node{
		Ref:    ref,
		Role:   orDefault(n.Role, "generic"),
		Name:   StripObjects(n.Name),
		States: agentStates(n.States),
		Bounds: n.Bounds,
		Parent: parent,
		Depth:  n.Depth,
	}
	if n.Value != nil {
		v := StripObjects(*n.Value)
		if utf8.RuneCountInString(v) > PreviewChars {
			v = Clip(v, PreviewChars, false)
			out.Truncated = true
		}
		out.Value = &v
	}
	if urls {
		out.URL = n.URL
	}
	out.RoleDesc, out.Description, out.Identifier, out.Help = n.RoleDesc, n.Description, n.Identifier, n.Help
	out.AXRole, out.ValueType, out.Placeholder, out.SelectedText = n.AXRole, n.ValueType, n.Placeholder, n.SelectedText
	if len(n.Actions) > 0 {
		out.Actions = append([]string(nil), n.Actions...)
	}
	return out
}

// DetailNodes lists every node of a snapshot in walk order, the window
// root and the menu bar included, with its raw states (settable among
// them) and full value: the input of the REPL's accessibility state.
func DetailNodes(s Snapshot) []protocol.Node {
	out := make([]protocol.Node, 0, len(s.Tree.Nodes))
	for i, n := range s.Tree.Nodes {
		p := NodeOf(n, s.Refs[i], s.parentRef(i), true)
		p.States = append([]string(nil), n.States...)
		if n.Value != nil {
			v := StripObjects(*n.Value)
			p.Value, p.Truncated = &v, false
		}
		out = append(out, p)
	}
	return out
}

// Nodes lists a snapshot's structured nodes for the given indexes (all when
// nil); the menu bar shows only with menus.
func Nodes(s Snapshot, idx []int, menus, urls bool) []protocol.Node {
	if idx == nil {
		idx = RenderOutline(s, OutlineOptions{Menus: menus, Budget: 1 << 30}).Shown
	}
	out := make([]protocol.Node, 0, len(idx))
	for _, i := range idx {
		out = append(out, NodeOf(s.Tree.Nodes[i], s.Refs[i], s.parentRef(i), urls))
	}
	return out
}

// VisionLine is one entry of a vision element list: labels capped at
// NameChars and values at VisionValueChars, both quoted and escaped.
func VisionLine(n int, role, name string, value *string) string {
	line := strconv.Itoa(n) + " " + orDefault(role, "generic") + " " + Field(name, NameChars)
	if value != nil {
		line += ": " + Field(*value, VisionValueChars)
	}
	return line
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
