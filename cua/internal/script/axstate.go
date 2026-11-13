package script

import (
	"sort"
	"strconv"
	"strings"

	"fennec/cua/internal/protocol"
)

// The REPL's accessibility state text (docs/codex-parity.md, section 5):
// a window header, one numbered line per element, the focused element (or
// the user's selected text); and after the first read through an app
// binding, a diff against the previous read.

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

// axRoleWords overrides the spoken role (AXRoleDescription) for raw roles
// whose words the reference prints differently: a group reads
// "container", a menu title in the menu bar shows by its name alone.
var axRoleWords = map[string]string{
	"AXGroup":       "container",
	"AXMenuBarItem": "",
}

// spokenRoles is the role a line shows when the read carries neither a raw
// role nor a role description (normalized role -> words).
var spokenRoles = map[string]string{
	"window": "standard window", "dialog": "dialog", "sheet": "sheet", "panel": "floating window",
	"button": "button", "checkbox": "checkbox", "switch": "switch", "radio": "radio button",
	"textbox": "text field", "combobox": "pop up button", "text": "text", "group": "container",
	"scrollarea": "scroll area", "scrollbar": "scroll bar", "menubar": "menu bar", "menu": "menu",
	"menuitem": "menu item", "menubaritem": "", "row": "row", "cell": "cell", "column": "column",
	"table": "table", "list": "list", "listitem": "list item", "link": "link", "image": "image",
	"slider": "slider", "tablist": "tab group", "tab": "tab", "toolbar": "toolbar", "document": "HTML content",
	"progressbar": "progress indicator", "tree": "outline", "treeitem": "row", "heading": "heading",
	"spinbutton": "stepper", "separator": "splitter", "valueindicator": "value indicator",
	"generic": "unknown", "application": "application", "radiogroup": "radio group", "grid": "grid",
}

// stateGroups lists the states a line shows in their order; each group
// shows at most one of its states.
var stateGroups = [][]string{{"selected", "selectable"}, {"disabled"}, {"expanded", "collapsed"}, {"settable"}}

// typedValues are the value kinds named after "settable" ("(settable,
// boolean)"); a string value is not named.
var typedValues = map[string]bool{"boolean": true, "integer": true, "float": true, "unknown": true}

// numericValues show bare in the name's place on an element with neither a
// name nor a description ("scroll bar (settable, float) 0").
var numericValues = map[string]bool{"integer": true, "float": true}

// selectionNote follows the user's selected text in place of the focused
// line.
const selectionNote = "Note: Pay special attention to the content selected by the user. If the user asks a question or refers to the content they are looking at on-screen, they might be referring to the selected content (but they might be referring to something else that's visible, too)."

// elidable says whether a node is an unnamed layout group with nothing to
// act on: it shows only while something shows inside it.
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

// roleWords is the role a line shows.
func roleWords(n protocol.Node) string {
	if w, ok := axRoleWords[n.AXRole]; ok {
		return w
	}
	if n.AXRole == "" {
		if n.Role == "group" || n.Role == "menubaritem" {
			return spokenRoles[n.Role]
		}
		if n.RoleDesc == "" {
			if w, ok := spokenRoles[n.Role]; ok {
				return w
			}
			return strings.ReplaceAll(n.Role, "_", " ")
		}
	}
	return n.RoleDesc
}

// stripScheme drops http:// and https:// from a URL as the state text does;
// other schemes (file:) stay.
func stripScheme(u string) string {
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(u, p) {
			return u[len(p):]
		}
	}
	return u
}

// lineText renders one element after its id: role words, states, name,
// then the labelled fields. Text keeps its line breaks.
func lineText(n protocol.Node) string {
	role := roleWords(n)
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
	switch {
	case n.ValueType == "boolean" && (value == "1" || value == "0"):
		value = map[string]string{"1": "on", "0": "off"}[value]
	case n.Role == "menuitem" && (value == "0" || value == "1"):
		value = ""
	}
	url := ""
	if n.Role == "link" {
		if n.URL != "" {
			value = stripScheme(n.URL)
		}
	} else if n.URL != "" {
		url = stripScheme(n.URL)
	}
	if name == "" && (n.Role == "text" || (desc == "" && numericValues[n.ValueType])) {
		name, value = value, ""
	}
	var states []string
	for _, g := range stateGroups {
		for _, s := range g {
			if hasState(n, s) {
				states = append(states, s)
				break
			}
		}
	}
	if hasState(n, "settable") && typedValues[n.ValueType] {
		states = append(states, n.ValueType)
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
	if n.Help != "" && n.Help != desc {
		fields = append(fields, "Help: "+n.Help)
	}
	if url != "" {
		fields = append(fields, "URL: "+url)
	}
	if value != "" {
		fields = append(fields, "Value: "+value)
	}
	if n.Identifier != "" {
		fields = append(fields, "ID: "+n.Identifier)
	}
	if n.Placeholder != "" {
		fields = append(fields, "Placeholder: "+n.Placeholder)
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
	return text
}

// shownNode is one line of a read.
type shownNode struct {
	ref   string
	depth int
	text  string
	node  protocol.Node
	// elide drops the line when nothing shows inside it.
	elide bool
}

// layout picks the nodes a read shows, in walk order, with their depths.
// A menu bar shows its titles (not the Apple menu); an open menu's items
// (showing) show beneath them. An unnamed layout group shows only while
// something shows inside it.
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
			if n.Role == "menubaritem" && n.Name == "Apple" {
				// The reference never lists the Apple menu.
				hidden[n.Ref] = true
				continue
			}
			if n.Role == "menuitem" && !hasState(n, "showing") {
				// A closed menu's item: neither it nor what it holds shows.
				hidden[n.Ref] = true
				continue
			}
			if n.Role == "menu" && elidable(n) {
				// A menu shows its items under its title.
				depth[n.Ref] = d
				continue
			}
		}
		out = append(out, shownNode{ref: n.Ref, depth: d, text: lineText(n), node: n, elide: elidable(n)})
		depth[n.Ref] = d + 1
	}
	// Drop elidable lines with nothing under them, innermost first.
	keep := make([]bool, len(out))
	nextDepth := -1 // the depth of the next kept line
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].elide && nextDepth <= out[i].depth {
			continue
		}
		keep[i] = true
		nextDepth = out[i].depth
	}
	kept := out[:0]
	for i, s := range out {
		if keep[i] {
			kept = append(kept, s)
		}
	}
	return kept
}

// headerTitle shortens a long window title in a header to its first and
// last 31 characters around an ellipsis.
func headerTitle(title string) string {
	r := []rune(title)
	if len(r) <= 63 {
		return title
	}
	return string(r[:31]) + "…" + string(r[len(r)-31:])
}

// windowHeader is a read's first line.
func windowHeader(title, app string) string {
	return "Window: \"" + headerTitle(title) + "\", App: " + app + "."
}

// read renders a read through m: the full state, or with diff a diff
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
		if focused < 0 && hasState(s.node, "focused") {
			focused = i
		}
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
	// The closing line: the user's selected text in the focused element,
	// else the focused element; nothing when no element has the focus.
	closing := ""
	if focused >= 0 {
		if sel := lines[focused].node.SelectedText; sel != "" {
			closing = "Selected text: ```\n" + sel + "\n```\n\n" + selectionNote
		} else {
			closing = "The focused UI element is " + strconv.Itoa(ids[focused]) + " " + lines[focused].text
		}
	}
	var b strings.Builder
	if !diff {
		b.WriteString(windowHeader(title, app))
		for i, s := range lines {
			b.WriteString("\n" + strings.Repeat("\t", s.depth) + strconv.Itoa(ids[i]) + " " + s.text)
		}
		if closing != "" {
			b.WriteString("\n\n" + closing)
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
			marker := ""
			switch {
			case !ok:
				marker = "+"
			case old.text != s.text || old.depth != s.depth:
				marker = "~"
			default:
				continue
			}
			changes = append(changes, marker+strings.Repeat("\t", s.depth)+strconv.Itoa(ids[i])+" "+s.text)
		}
		if len(removed) == 0 && len(changes) == 0 {
			b.WriteString("There has been no change in the accessibility tree for Window: \"" + headerTitle(title) + "\".")
		} else {
			b.WriteString("The following is a diff from the previous accessibility tree for Window: \"" + headerTitle(title) + "\" with ~ and + representing changed and added elements, respectively. Removed elements are summarized by ID range.")
			if len(removed) > 0 {
				b.WriteString("\nRemoved element IDs: " + idRanges(removed))
			}
			for _, c := range changes {
				b.WriteString("\n" + c)
			}
		}
		if closing != "" {
			b.WriteString("\n" + closing)
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
