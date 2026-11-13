package script

import (
	"strings"
	"testing"

	"fennec/cua/internal/protocol"
)

// The state text rules confirmed against recorded Codex sessions
// (docs/codex-parity.md, section 5). Every tree here is synthetic.

func sv(s string) *string { return &s }

func richTree() []protocol.Node {
	return []protocol.Node{
		{Ref: "w", Role: "window", AXRole: "AXWindow", RoleDesc: "standard window", Name: "Orders", Actions: []string{"Raise"}},
		// A group with something inside reads "container"; a group with
		// nothing inside does not show.
		{Ref: "g1", Role: "group", AXRole: "AXGroup", RoleDesc: "group", Parent: "w"},
		{Ref: "b1", Role: "button", AXRole: "AXButton", RoleDesc: "button", Name: "Refresh", States: []string{"collapsed"}, Parent: "g1"},
		{Ref: "g2", Role: "group", AXRole: "AXGroup", RoleDesc: "group", Parent: "w"},
		{Ref: "g3", Role: "group", AXRole: "AXGroup", RoleDesc: "group", Parent: "g2"},
		// Boolean values read on/off, with the value kind after settable.
		{Ref: "s1", Role: "switch", AXRole: "AXCheckBox", RoleDesc: "switch", Name: "Sync", Value: sv("1"), ValueType: "boolean", States: []string{"settable"}, Parent: "w"},
		{Ref: "s2", Role: "switch", AXRole: "AXCheckBox", RoleDesc: "switch", Name: "Beta", Value: sv("0"), ValueType: "boolean", States: []string{"disabled", "settable"}, Parent: "w"},
		// An unnamed number shows bare in the name's place.
		{Ref: "sb", Role: "scrollbar", AXRole: "AXScrollBar", RoleDesc: "scroll bar", Value: sv("0.25"), ValueType: "float", States: []string{"settable"}, Parent: "w"},
		// State order: selected/selectable, disabled, expanded/collapsed,
		// settable, the value kind.
		{Ref: "r1", Role: "row", AXRole: "AXRow", RoleDesc: "row", Description: "folder", Value: sv("Inbox"), States: []string{"expanded", "selectable"}, Actions: []string{"Collapse"}, Parent: "w"},
		{Ref: "r2", Role: "row", AXRole: "AXRow", RoleDesc: "row", Name: "Archive", States: []string{"selected"}, Parent: "w"},
		{Ref: "rg", Role: "radiogroup", AXRole: "AXRadioGroup", Name: "Mode", ValueType: "unknown", States: []string{"settable"}, Parent: "w"},
		{Ref: "rb", Role: "radio", AXRole: "AXRadioButton", RoleDesc: "radio button", Name: "Fast", Value: sv("1"), ValueType: "integer", Parent: "rg"},
		// Field order: Description, Help, URL, Value, ID, Placeholder,
		// Secondary Actions.
		{Ref: "tf", Role: "textbox", AXRole: "AXTextField", RoleDesc: "text field", Description: "Search", Help: "Find orders", Value: sv("lamp"), ValueType: "string", Identifier: "search", Placeholder: "Search orders", States: []string{"settable", "focused"}, SelectedText: "", Parent: "w"},
		{Ref: "wa", Role: "document", AXRole: "AXWebArea", RoleDesc: "HTML content", Description: "Orders", URL: "https://shop.example.test/orders", Parent: "w"},
		// A link's address is its value, without http(s)://.
		{Ref: "ln", Role: "link", AXRole: "AXLink", RoleDesc: "link", Name: "Help center", URL: "https://help.example.test/start", Parent: "wa"},
		// Text keeps its line breaks.
		{Ref: "tx", Role: "text", AXRole: "AXStaticText", RoleDesc: "text", Value: sv("Line one\nLine two"), Parent: "wa"},
		{Ref: "fi", Role: "textbox", AXRole: "AXTextField", RoleDesc: "text field", Value: sv("Report.pdf"), ValueType: "string", URL: "file:///Users/test/Report.pdf", States: []string{"settable"}, Actions: []string{"Open Finder item"}, Parent: "w"},
		{Ref: "mb", Role: "menubar", AXRole: "AXMenuBar", RoleDesc: "menu bar"},
		{Ref: "ap", Role: "menubaritem", AXRole: "AXMenuBarItem", RoleDesc: "menu bar item", Name: "Apple", Parent: "mb"},
		{Ref: "m1", Role: "menubaritem", AXRole: "AXMenuBarItem", RoleDesc: "menu bar item", Name: "Orders", Parent: "mb"},
		{Ref: "m2", Role: "menubaritem", AXRole: "AXMenuBarItem", RoleDesc: "menu bar item", Name: "Edit", Parent: "mb"},
	}
}

func withDepths(nodes []protocol.Node) []protocol.Node {
	depth := map[string]int{}
	for i := range nodes {
		if nodes[i].Parent != "" {
			nodes[i].Depth = depth[nodes[i].Parent] + 1
		}
		depth[nodes[i].Ref] = nodes[i].Depth
	}
	return nodes
}

func observed(title string, nodes []protocol.Node) *protocol.ObserveResult {
	return &protocol.ObserveResult{Window: &protocol.Window{Ref: "w", App: "Orders", Title: title}, Nodes: withDepths(nodes)}
}

const richState = "Window: \"Orders\", App: Orders.\n" +
	"0 standard window Orders, Secondary Actions: Raise\n" +
	"\t1 container\n" +
	"\t\t2 button (collapsed) Refresh\n" +
	"\t3 switch (settable, boolean) Sync, Value: on\n" +
	"\t4 switch (disabled, settable, boolean) Beta, Value: off\n" +
	"\t5 scroll bar (settable, float) 0.25\n" +
	"\t6 row (selectable, expanded) Description: folder, Value: Inbox, Secondary Actions: Collapse\n" +
	"\t7 row (selected) Archive\n" +
	"\t8 (settable, unknown) Mode\n" +
	"\t\t9 radio button Fast, Value: 1\n" +
	"\t10 text field (settable) Description: Search, Help: Find orders, Value: lamp, ID: search, Placeholder: Search orders\n" +
	"\t11 HTML content Description: Orders, URL: shop.example.test/orders\n" +
	"\t\t12 link Help center, Value: help.example.test/start\n" +
	"\t\t13 text Line one\nLine two\n" +
	"\t14 text field (settable) URL: file:///Users/test/Report.pdf, Value: Report.pdf, Secondary Actions: Open Finder item\n" +
	"15 menu bar\n" +
	"\t16 Orders\n" +
	"\t17 Edit\n" +
	"\n" +
	"The focused UI element is 10 text field (settable) Description: Search, Help: Find orders, Value: lamp, ID: search, Placeholder: Search orders"

func TestAXStateConfirmedRules(t *testing.T) {
	m := newAXMemory()
	if got := m.read(observed("Orders", richTree()), true); got != richState {
		t.Errorf("full state:\n%s\nwant:\n%s", got, richState)
	}
}

// No change: one line naming the window, then the focused line.
func TestAXStateNoChange(t *testing.T) {
	m := newAXMemory()
	m.read(observed("Orders", richTree()), true)
	want := "There has been no change in the accessibility tree for Window: \"Orders\".\n" +
		"The focused UI element is 10 text field (settable) Description: Search, Help: Find orders, Value: lamp, ID: search, Placeholder: Search orders"
	if got := m.read(observed("Orders", richTree()), true); got != want {
		t.Errorf("unchanged:\n%s\nwant:\n%s", got, want)
	}
}

// A diff line is its marker followed by the element's depth in tabs; the
// focused line follows without a blank line.
func TestAXStateDiffDepth(t *testing.T) {
	m := newAXMemory()
	m.read(observed("Orders", richTree()), true)
	next := richTree()
	next[0].Name = "Orders (2)"
	next[2].States = []string{"expanded"}
	next = append(next[:16], append([]protocol.Node{{Ref: "tx2", Role: "text", AXRole: "AXStaticText", RoleDesc: "text", Value: sv("Saved"), Parent: "wa"}}, next[16:]...)...)
	next = append(next[:6], next[7:]...) // the switch Beta goes
	want := "The following is a diff from the previous accessibility tree for Window: \"Orders\" with ~ and + representing changed and added elements, respectively. Removed elements are summarized by ID range.\n" +
		"Removed element IDs: 4\n" +
		"~0 standard window Orders (2), Secondary Actions: Raise\n" +
		"~\t\t2 button (expanded) Refresh\n" +
		"+\t\t18 text Saved\n" +
		"The focused UI element is 10 text field (settable) Description: Search, Help: Find orders, Value: lamp, ID: search, Placeholder: Search orders"
	if got := m.read(observed("Orders", next), true); got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}
}

// Without a focused element there is no closing line; the user's selected
// text in the focused element replaces the focused line.
func TestAXStateFocusVariants(t *testing.T) {
	tree := []protocol.Node{
		{Ref: "w", Role: "window", AXRole: "AXWindow", RoleDesc: "standard window", Name: "Notes"},
		{Ref: "ta", Role: "textbox", AXRole: "AXTextArea", RoleDesc: "text entry area", Value: sv("alpha beta"), ValueType: "string", States: []string{"settable"}, Parent: "w"},
	}
	m := newAXMemory()
	want := "Window: \"Notes\", App: Orders.\n0 standard window Notes\n\t1 text entry area (settable) Value: alpha beta"
	if got := m.read(observed("Notes", tree), true); got != want {
		t.Errorf("no focus:\n%q\nwant:\n%q", got, want)
	}
	tree[1].States = []string{"settable", "focused"}
	tree[1].SelectedText = "beta"
	m = newAXMemory()
	want += "\n\nSelected text: ```\nbeta\n```\n\n" + selectionNote
	if got := m.read(observed("Notes", tree), true); got != want {
		t.Errorf("selection:\n%q\nwant:\n%q", got, want)
	}
	if got := m.read(observed("Notes", tree), true); got != "There has been no change in the accessibility tree for Window: \"Notes\".\nSelected text: ```\nbeta\n```\n\n"+selectionNote {
		t.Errorf("selection after no change: %q", got)
	}
}

// A long window title is cut in the header to its first and last 31
// characters.
func TestAXStateLongTitle(t *testing.T) {
	title := strings.Repeat("a", 31) + strings.Repeat("m", 40) + strings.Repeat("z", 31)
	if got := windowHeader(title, "App"); got != "Window: \""+strings.Repeat("a", 31)+"…"+strings.Repeat("z", 31)+"\", App: App." {
		t.Errorf("header %q", got)
	}
	short := strings.Repeat("b", 63)
	if got := windowHeader(short, "App"); got != "Window: \""+short+"\", App: App." {
		t.Errorf("short header %q", got)
	}
}
