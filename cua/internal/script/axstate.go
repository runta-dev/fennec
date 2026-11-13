package script

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"fennec/cua/internal/protocol"
)

// The REPL's accessibility state text (docs/codex-parity.md, section 5):
// a window header, one numbered line per element, the focused element; and
// after the first read through an app binding, a diff against the previous
// read.

// axMemory is what an app binding remembers between state reads: the ids it
// handed out (stable for unchanged elements), the lines it showed and the
// window they belong to.
type axMemory struct {
	window string // the window's ref
	title  string
	ids    map[string]int // element ref -> id
	next   int            // the next fresh id
	shown  map[int]axLine // the latest read
	order  []int
	// byID resolves ids of the latest read to its elements.
	byID map[int]axElement
}

type axLine struct {
	depth int
	text  string
}

// axElement is what an id stands for.
type axElement struct {
	Ref     string
	Role    string
	Value   string
	Actions []string
}

func newAXMemory() *axMemory {
	return &axMemory{ids: map[string]int{}, shown: map[int]axLine{}, byID: map[int]axElement{}}
}

// spokenRoles is the role a line shows when the app gave no role
// description.
var spokenRoles = map[string]string{
	"window": "standard window", "dialog": "dialog", "sheet": "sheet", "panel": "floating window",
	"button": "button", "checkbox": "checkbox", "switch": "switch", "radio": "radio button",
	"textbox": "text field", "combobox": "pop up button", "text": "text", "group": "group",
	"scrollarea": "scroll area", "scrollbar": "scroll bar", "menubar": "menu bar", "menu": "menu",
	"menuitem": "menu item", "menubaritem": "menu bar item", "row": "row", "cell": "cell", "column": "column",
	"table": "table", "list": "list", "listitem": "list item", "link": "link", "image": "image",
	"slider": "slider", "tablist": "tab group", "tab": "tab", "toolbar": "toolbar", "document": "HTML content",
	"progressbar": "progress indicator", "tree": "outline", "treeitem": "row", "heading": "heading",
	"spinbutton": "stepper", "separator": "splitter", "valueindicator": "value indicator",
	"generic": "unknown", "application": "application", "radiogroup": "radio group", "grid": "grid",
}

// stateOrder lists the states a line shows, in their order.
var stateOrder = []string{"selected", "disabled", "settable", "selectable", "expanded", "collapsed", "checked"}

// elidable says whether a node is an unnamed layout group with nothing to
// act on: it gets no line, and its children take its place.
func elidable(n protocol.Node) bool {
	if n.Role != "group" && n.Role != "generic" && n.Role != "menu" {
		return false
	}
	if n.Name != "" || n.Description != "" || n.Identifier != "" || len(n.Actions) > 0 {
		return false
	}
	if n.Value != nil && *n.Value != "" {
		return false
	}
	for _, s := range n.States {
		if s == "focused" || s == "selected" || s == "settable" {
			return false
		}
	}
	return true
}

func hasState(n protocol.Node, s string) bool {
	for _, x := range n.States {
		if x == s {
			return true
		}
	}
	return false
}

// lineText renders one element after its id: role words, states, name,
// then the labelled fields.
func lineText(n protocol.Node) string {
	role := n.RoleDesc
	if role == "" {
		role = spokenRoles[n.Role]
		if role == "" {
			role = strings.ReplaceAll(n.Role, "_", " ")
		}
	}
	if n.Role == "menubaritem" {
		role = "" // a menu title shows by its name alone
	}
	name := n.Name
	desc := n.Description
	if name != "" && name == desc {
		// The reader names an untitled element by its description.
		name = ""
	}
	value := ""
	if n.Value != nil {
		value = *n.Value
	}
	if n.Role == "text" && name == "" {
		name, value = value, ""
	}
	switch n.Role {
	case "checkbox", "radio", "switch", "menuitem":
		if value == "0" || value == "1" {
			value = ""
		}
	}
	var states []string
	for _, s := range stateOrder {
		if hasState(n, s) {
			states = append(states, s)
		}
	}
	head := []string{}
	if role != "" {
		head = append(head, role)
	}
	if len(states) > 0 {
		head = append(head, "("+strings.Join(states, ", ")+")")
	}
	if name != "" {
		head = append(head, name)
	}
	var fields []string
	if desc != "" {
		fields = append(fields, "Description: "+desc)
	}
	if n.Identifier != "" {
		fields = append(fields, "ID: "+n.Identifier)
	}
	if n.Help != "" && n.Help != desc {
		fields = append(fields, "Help: "+n.Help)
	}
	if n.URL != "" {
		fields = append(fields, "URL: "+n.URL)
	}
	if value != "" {
		fields = append(fields, "Value: "+oneLine(value))
	}
	if len(n.Actions) > 0 {
		fields = append(fields, "Secondary Actions: "+strings.Join(n.Actions, ", "))
	}
	text := strings.Join(head, " ")
	if len(fields) > 0 {
		sep := " "
		if name != "" {
			sep = ", "
		}
		if text == "" {
			sep = ""
		}
		text += sep + strings.Join(fields, ", ")
	}
	return oneLine(text)
}

// oneLine keeps a line on one line.
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r\n", "\\n", "\n", "\\n", "\r", "\\n").Replace(s)
}

// shownNode is one line of a read.
type shownNode struct {
	ref   string
	depth int
	text  string
	node  protocol.Node
}

// layout picks the nodes a read shows, in walk order, with their depths
// after elided groups lift their children. A menu bar shows its titles;
// an open menu's items (showing) show beneath them.
func layout(nodes []protocol.Node) []shownNode {
	depth := map[string]int{} // ref -> the depth its children show at
	inMenuBar := map[string]bool{}
	hidden := map[string]bool{}
	var out []shownNode
	for _, n := range nodes {
		if n.Parent != "" && hidden[n.Parent] {
			hidden[n.Ref] = true
			continue
		}
		d := 0
		if n.Parent != "" {
			d = depth[n.Parent]
		}
		if n.Role == "menubar" || (n.Parent != "" && inMenuBar[n.Parent]) {
			inMenuBar[n.Ref] = true
			if n.Role == "menuitem" && !hasState(n, "showing") {
				// A closed menu's item: neither it nor what it holds shows.
				hidden[n.Ref] = true
				continue
			}
		}
		if elidable(n) {
			depth[n.Ref] = d
			continue
		}
		out = append(out, shownNode{ref: n.Ref, depth: d, text: lineText(n), node: n})
		depth[n.Ref] = d + 1
	}
	return out
}

// windowHeader is a read's first line.
func windowHeader(title, app string) string {
	return fmt.Sprintf("Window: %q, App: %s.", title, app)
}

// axRead renders a read through m: the full state, or with diff a diff
// against m's previous read. A read of another window than m's last is
// always full and starts the ids again from 0.
func (m *axMemory) read(res *protocol.ObserveResult, diff bool) string {
	title, app, wref := "", "", ""
	if res.Window != nil {
		title, app, wref = res.Window.Title, res.Window.App, res.Window.Ref
	}
	if wref != m.window {
		*m = *newAXMemory()
		m.window, diff = wref, false
	}
	first := len(m.shown) == 0 && len(m.ids) == 0
	if first {
		diff = false
	}
	lines := layout(res.Nodes)
	focused := -1
	ids := make([]int, len(lines))
	for i, s := range lines {
		id, ok := m.ids[s.ref]
		if !ok {
			id = m.next
			m.ids[s.ref] = id
			m.next++
		}
		ids[i] = id
		if hasState(s.node, "focused") {
			focused = i
		}
	}
	if focused < 0 && len(lines) > 0 {
		focused = 0
	}
	shown := map[int]axLine{}
	byID := map[int]axElement{}
	order := make([]int, len(lines))
	for i, s := range lines {
		shown[ids[i]] = axLine{depth: s.depth, text: s.text}
		order[i] = ids[i]
		v := ""
		if s.node.Value != nil {
			v = *s.node.Value
		}
		byID[ids[i]] = axElement{Ref: s.ref, Role: s.node.Role, Value: v, Actions: s.node.Actions}
	}
	focusLine := ""
	if focused >= 0 {
		focusLine = "The focused UI element is " + strconv.Itoa(ids[focused]) + " " + lines[focused].text
	}
	var b strings.Builder
	if !diff {
		b.WriteString(windowHeader(title, app))
		for i, s := range lines {
			b.WriteString("\n" + strings.Repeat("\t", s.depth) + strconv.Itoa(ids[i]) + " " + s.text)
		}
		if focusLine != "" {
			b.WriteString("\n\n" + focusLine)
		}
	} else {
		var removed []int
		for id := range m.shown {
			if _, ok := shown[id]; !ok {
				removed = append(removed, id)
			}
		}
		sort.Ints(removed)
		var changes []string
		for i, s := range lines {
			old, ok := m.shown[ids[i]]
			switch {
			case !ok:
				changes = append(changes, "+\t"+strings.Repeat("\t", s.depth)+strconv.Itoa(ids[i])+" "+s.text)
			case old.text != s.text || old.depth != s.depth:
				changes = append(changes, "~\t"+strconv.Itoa(ids[i])+" "+s.text)
			}
		}
		if len(removed) == 0 && len(changes) == 0 {
			b.WriteString(fmt.Sprintf("The accessibility tree for Window: %q has not changed since the previous read.", title))
		} else {
			b.WriteString(fmt.Sprintf("The following is a diff from the previous accessibility tree for Window: %q with ~ and + representing changed and added elements, respectively. Removed elements are summarized by ID range.", title))
			if len(removed) > 0 {
				b.WriteString("\nRemoved element IDs: " + idRanges(removed))
			}
			for _, c := range changes {
				b.WriteString("\n" + c)
			}
			if focusLine != "" {
				b.WriteString("\n" + focusLine)
			}
		}
	}
	m.title, m.shown, m.order, m.byID = title, shown, order, byID
	return b.String()
}

// idRanges renders sorted ids as "2-3, 7".
func idRanges(ids []int) string {
	var parts []string
	for i := 0; i < len(ids); {
		j := i
		for j+1 < len(ids) && ids[j+1] == ids[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, strconv.Itoa(ids[i])+"-"+strconv.Itoa(ids[j]))
		} else {
			parts = append(parts, strconv.Itoa(ids[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}
