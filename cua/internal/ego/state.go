package ego

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// A tab's state text, in the format Codex shows for Chrome tabs
// (docs/codex-parity.md, section 8): a "Browser tab:" header, one numbered
// line per element indented by depth with the role words macOS speaks for
// Chromium's roles, the focused element last; after the first read through
// a binding, a diff with stable ids.

// webRoles maps Chromium roles to the role words of a line. Roles not
// listed read "container".
var webRoles = map[string]string{
	"RootWebArea": "AXWebArea", "WebArea": "AXWebArea",
	"StaticText": "text", "heading": "heading", "link": "link", "button": "button",
	"image": "image", "img": "image", "graphics-document": "image", "graphics-symbol": "image", "svg": "image", "canvas": "image",
	"textbox": "text field", "searchbox": "search text field", "combobox": "combo box",
	"checkbox": "checkbox", "radio": "radio button", "switch": "switch", "spinbutton": "stepper",
	"slider": "slider", "progressbar": "progress indicator", "meter": "level indicator",
	"list": "content list", "listbox": "list", "option": "option", "ListMarker": "AXListMarker",
	"table": "table", "grid": "table", "treegrid": "table", "row": "row",
	"cell": "cell", "gridcell": "cell", "columnheader": "cell", "rowheader": "cell",
	"tablist": "tab group", "tab": "tab", "tree": "outline", "treeitem": "row",
	"menu": "menu", "menubar": "menu bar", "menuitem": "menu item", "menuitemcheckbox": "menu item", "menuitemradio": "menu item",
	"toolbar": "toolbar", "dialog": "dialog", "alertdialog": "dialog", "separator": "splitter",
	"radiogroup": "radio group", "scrollbar": "scroll bar",
}

// skipped roles never show, nor what they hold.
var skippedRoles = map[string]bool{"InlineTextBox": true, "LineBreak": true, "MenuListPopup": true}

// fieldRoles hold an editor whose inner nodes are not shown.
var fieldRoles = map[string]bool{"textbox": true, "searchbox": true, "spinbutton": true, "slider": true}

// valueRoles show their value.
var valueRoles = map[string]bool{
	"textbox": true, "searchbox": true, "combobox": true, "spinbutton": true, "slider": true,
	"progressbar": true, "meter": true, "scrollbar": true,
}

// checkRoles show their checked state as a 0/1 value.
var checkRoles = map[string]bool{"checkbox": true, "radio": true, "switch": true, "menuitemcheckbox": true, "menuitemradio": true}

// descRoles show an attribute-given name (aria-label, title) as their
// Description rather than as their name.
var descRoles = map[string]bool{
	"button": true, "checkbox": true, "radio": true, "switch": true, "combobox": true, "tab": true,
	"menuitem": true, "menuitemcheckbox": true, "menuitemradio": true, "spinbutton": true, "slider": true,
	"textbox": true, "searchbox": true, "link": true, "option": true,
}

// isSelect says whether a combobox is a native <select>, which macOS
// speaks as a pop up button.
func isSelect(n *AXNode) bool {
	for _, c := range n.Children {
		if c.Role == "MenuListPopup" {
			return true
		}
	}
	return false
}

func roleWords(n *AXNode) string {
	switch n.Role {
	case "combobox":
		if isSelect(n) {
			return "pop up button"
		}
	case "button":
		if hp, ok := n.Prop("hasPopup"); ok && hp != "" && hp != "false" {
			return "pop up button"
		}
		if _, ok := n.Prop("pressed"); ok {
			return "toggle button"
		}
	case "textbox":
		if n.True("multiline") {
			return "text entry area"
		}
	}
	if w, ok := webRoles[n.Role]; ok {
		return w
	}
	return "container"
}

// stripScheme drops http:// and https:// as the state text does.
func stripScheme(u string) string {
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(u, p) {
			return u[len(p):]
		}
	}
	return u
}

// shortURL keeps data: URLs short; they can hold a whole page.
func shortURL(u string) string {
	u = stripScheme(u)
	if strings.HasPrefix(u, "data:") && len(u) > 64 {
		head, _, _ := strings.Cut(u, ",")
		return head + ",…"
	}
	return u
}

// oneLine keeps a field on one line.
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r\n", "\\n", "\n", "\\n", "\r", "\\n").Replace(s)
}

// Element is what an element id of a read stands for.
type Element struct {
	Backend  int
	Role     string // Chromium's role
	Words    string // the role words shown
	Name     string
	Value    string
	Actions  []string
	Editable bool
	URL      string
}

// vnode is a node to show.
type vnode struct {
	n        *AXNode
	key      string
	text     string
	el       Element
	children []*vnode
}

// collapsibleLink says whether a link holds only text: it then shows as one
// line with its URL as the value.
func collapsibleLink(n *AXNode) bool {
	if n.Role != "link" || n.Name == "" {
		return false
	}
	if u, _ := n.Prop("url"); u == "" {
		return false
	}
	ok := true
	var walk func(c *AXNode)
	walk = func(c *AXNode) {
		for _, k := range c.Children {
			switch {
			case k.Role == "StaticText" || k.Role == "InlineTextBox" || k.Role == "LineBreak":
			case k.Ignored || k.Role == "none" || k.Role == "generic":
				walk(k)
			default:
				ok = false
			}
		}
	}
	walk(n)
	return ok
}

// lineText renders one element after its id.
func lineText(n *AXNode, words string, collapsed bool, ids map[int]string) (string, Element) {
	el := Element{Backend: n.Backend, Role: n.Role, Words: words, Name: n.Name}
	name, desc := n.Name, ""
	isRoot := n.Role == "RootWebArea" || n.Role == "WebArea"
	if n.Role == "link" {
		if collapsed || name == "" {
			if collapsed {
				desc, name = name, ""
			} else if u, _ := n.Prop("url"); u != "" {
				name = u
			}
		} else if n.NameFrom == "attribute" {
			desc, name = name, ""
		}
	} else if descRoles[n.Role] && n.NameFrom == "attribute" && n.NameAttr != "alt" {
		desc, name = name, ""
	}
	value := ""
	switch {
	case checkRoles[n.Role]:
		switch c, _ := n.Prop("checked"); c {
		case "true":
			value = "1"
		case "mixed":
			value = "2"
		default:
			value = "0"
		}
	case n.Role == "heading":
		value, _ = n.Prop("level")
	case valueRoles[n.Role]:
		value = n.Value
		if value == "" {
			value, _ = n.Prop("valuetext")
		}
	}
	el.Value = value
	var states []string
	if s, ok := n.Prop("selected"); ok {
		if s == "true" {
			states = append(states, "selected")
		} else {
			states = append(states, "selectable")
		}
	}
	if n.True("disabled") {
		states = append(states, "disabled")
	}
	var actions []string
	if e, ok := n.Prop("expanded"); ok {
		if e == "true" {
			states = append(states, "expanded")
			actions = append(actions, "Collapse")
		} else {
			states = append(states, "collapsed")
			actions = append(actions, "Expand")
		}
	}
	editable := false
	if (fieldRoles[n.Role] || (n.Role == "combobox" && !isSelect(n))) && n.True("settable") && !n.True("disabled") && !n.True("readonly") {
		states = append(states, "settable")
		editable = true
	}
	if ed, _ := n.Prop("editable"); ed != "" {
		editable = true
	}
	el.Editable, el.Actions = editable, actions
	head := []string{words}
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
	linkURL := ""
	if n.Role == "link" && collapsed {
		u, _ := n.Prop("url")
		linkURL = shortURL(u)
		el.URL = u
	}
	if value != "" {
		fields = append(fields, "Value: "+oneLine(value))
	}
	if n.Desc != "" && n.Desc != n.Name {
		fields = append(fields, "Help: "+oneLine(n.Desc))
	}
	if id := ids[n.Backend]; id != "" && !isRoot {
		fields = append(fields, "ID: "+id)
	}
	if isRoot {
		u, _ := n.Prop("url")
		el.URL = u
		fields = append(fields, "URL: "+shortURL(u))
	}
	if linkURL != "" {
		fields = append(fields, "Value: "+linkURL)
	}
	if len(actions) > 0 {
		fields = append(fields, "Secondary Actions: "+strings.Join(actions, ", "))
	}
	text := strings.Join(head, " ")
	if len(fields) > 0 {
		sep := " "
		if name != "" {
			sep = ", "
		}
		text += sep + strings.Join(fields, ", ")
	}
	return oneLine(text), el
}

// layout builds the shown tree: ignored nodes and unnamed single-child
// containers give way to their children; field editors, inline text boxes
// and closed select lists do not show.
func layout(t *AXTree, ids map[int]string) *vnode {
	var build func(n *AXNode) []*vnode
	build = func(n *AXNode) []*vnode {
		if skippedRoles[n.Role] {
			return nil
		}
		var kids []*vnode
		collapsed := collapsibleLink(n)
		if !collapsed && !fieldRoles[n.Role] && !(n.Role == "combobox" && !isSelect(n)) {
			for _, c := range n.Children {
				kids = append(kids, build(c)...)
			}
		}
		if n.Ignored || n.Role == "none" || n.Role == "presentation" {
			return kids
		}
		if n.Role == "StaticText" && strings.TrimSpace(n.Name) == "" {
			return nil
		}
		words := roleWords(n)
		if words == "container" && n.Name == "" && n.Desc == "" && !n.True("focusable") && n.Role != "RootWebArea" {
			if _, ok := n.Prop("expanded"); !ok && len(kids) <= 1 {
				return kids
			}
		}
		text, el := lineText(n, words, collapsed, ids)
		key := "n" + n.ID
		if n.Backend != 0 {
			key = "b" + strconv.Itoa(n.Backend)
		}
		return []*vnode{{n: n, key: key, text: text, el: el, children: kids}}
	}
	out := build(t.Root)
	if len(out) == 1 && out[0].n == t.Root {
		return out[0]
	}
	// The root always shows.
	text, el := lineText(t.Root, "AXWebArea", false, ids)
	return &vnode{n: t.Root, key: "b" + strconv.Itoa(t.Root.Backend), text: text, el: el, children: out}
}

// shownLine is one line of a read.
type shownLine struct {
	key   string
	depth int
	text  string
	el    Element
	focus bool
}

func flatten(v *vnode) []shownLine {
	var out []shownLine
	var walk func(v *vnode, d int)
	walk = func(v *vnode, d int) {
		out = append(out, shownLine{key: v.key, depth: d, text: v.text, el: v.el, focus: v.n.True("focused")})
		for _, c := range v.children {
			walk(c, d+1)
		}
	}
	walk(v, 0)
	return out
}

// Memory is what a tab binding remembers between reads.
type Memory struct {
	doc   string         // the document's root key
	url   string         // the URL of the last read
	ids   map[string]int // element key -> id
	next  int
	shown map[int]axLine
	byID  map[int]Element
}

type axLine struct {
	depth int
	text  string
}

// NewMemory starts a binding's memory.
func NewMemory() *Memory {
	return &Memory{ids: map[string]int{}, shown: map[int]axLine{}, byID: map[int]Element{}}
}

// Element resolves an id of the latest read.
func (m *Memory) Element(id int) (Element, bool) {
	e, ok := m.byID[id]
	return e, ok
}

// Read renders a read: the full state, or a diff against the previous
// read when diff is set and the page is the same. A new document starts
// the ids again from 0; a changed URL or a change to more than half the
// lines gives the full state with the ids kept.
func (m *Memory) Read(tabID, title, url string, t *AXTree, ids map[int]string, diff bool) string {
	root := layout(t, ids)
	lines := flatten(root)
	if root.key != m.doc {
		*m = *NewMemory()
		m.doc = root.key
		diff = false
	}
	if len(m.shown) == 0 || url != m.url {
		diff = false
	}
	idOf := make([]int, len(lines))
	focused := -1
	for i, l := range lines {
		id, ok := m.ids[l.key]
		if !ok {
			id = m.next
			m.ids[l.key] = id
			m.next++
		}
		idOf[i] = id
		if l.focus && focused < 0 {
			focused = i
		}
	}
	if focused < 0 {
		focused = 0
	}
	shown := map[int]axLine{}
	byID := map[int]Element{}
	for i, l := range lines {
		shown[idOf[i]] = axLine{depth: l.depth, text: l.text}
		byID[idOf[i]] = l.el
	}
	var changes []string
	var removed []int
	if diff {
		for id := range m.shown {
			if _, ok := shown[id]; !ok {
				removed = append(removed, id)
			}
		}
		sort.Ints(removed)
		for i, l := range lines {
			old, ok := m.shown[idOf[i]]
			marker := ""
			switch {
			case !ok:
				marker = "+"
			case old.text != l.text || old.depth != l.depth:
				marker = "~"
			default:
				continue
			}
			changes = append(changes, marker+strings.Repeat("\t", l.depth)+strconv.Itoa(idOf[i])+" "+l.text)
		}
		if 2*len(changes) > len(lines) {
			diff = false
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Browser tab: %s, Title: \"%s\", URL: \"%s\".", tabID, oneLine(title), oneLine(url))
	focusLine := "The focused UI element is " + strconv.Itoa(idOf[focused]) + " " + lines[focused].text
	switch {
	case !diff:
		for i, l := range lines {
			b.WriteString("\n" + strings.Repeat("\t", l.depth) + strconv.Itoa(idOf[i]) + " " + l.text)
		}
		b.WriteString("\n\n" + focusLine)
	case len(changes) == 0 && len(removed) == 0:
		b.WriteString("\nThe accessibility tree has not changed since the previous read.")
	default:
		b.WriteString("\nThe following is a diff from the previous accessibility tree with ~ and + representing changed and added elements, respectively. Removed elements are summarized by ID range.")
		if len(removed) > 0 {
			b.WriteString("\nRemoved element IDs: " + idRanges(removed))
		}
		for _, c := range changes {
			b.WriteString("\n" + c)
		}
		b.WriteString("\n\n" + focusLine)
	}
	m.url, m.shown, m.byID = url, shown, byID
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

// DOMIDs maps backend node ids to their elements' id attributes.
func DOMIDs(d *DOM) map[int]string {
	out := map[int]string{}
	if d == nil {
		return out
	}
	for _, n := range d.Order {
		if id := n.Attrs["id"]; id != "" && n.Type == 1 {
			out[n.Backend] = id
		}
	}
	return out
}
