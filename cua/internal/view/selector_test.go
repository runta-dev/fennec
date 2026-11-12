package view

import (
	"reflect"
	"strings"
	"testing"
)

func mustSel(t *testing.T, s string) Selector {
	t.Helper()
	sel, err := ParseSelector(s)
	if err != nil {
		t.Fatalf("ParseSelector(%q): %v", s, err)
	}
	return sel
}

func TestParseSelector(t *testing.T) {
	cases := map[string]Selector{
		`button "Save"`:                    {Roles: []string{"button"}, Name: "Save"},
		`"Save"`:                           {Name: "Save"},
		`tab|button "Bold"`:                {Roles: []string{"tab", "button"}, Name: "Bold"},
		`secure textbox`:                   {Roles: []string{"textbox"}, States: []string{"protected"}},
		`checked checkbox "Wrap"`:          {Roles: []string{"checkbox"}, Name: "Wrap", States: []string{"checked"}},
		`unchecked checkbox`:               {Roles: []string{"checkbox"}, States: []string{"!checked"}},
		`button "OK" #2`:                   {Roles: []string{"button"}, Name: "OK", Index: 2},
		`the Bold button`:                  {Roles: []string{"button"}, Name: "Bold"},
		`Save`:                             {Name: "Save"},
		`Sign in`:                          {Name: "Sign in"},
		`Search button`:                    {Roles: []string{"button"}, Name: "Search"},
		`field "Say \"hi\""`:               {Roles: []string{"textbox"}, Name: `Say "hi"`},
		`textbox "Name" in sheet "Export"`: {Roles: []string{"textbox"}, Name: "Name", In: &Selector{Roles: []string{"sheet"}, Name: "Export"}},
		`"In" in group "Box" #1`:           {Name: "In", In: &Selector{Roles: []string{"group"}, Name: "Box", Index: 1}},
	}
	for in, want := range cases {
		got := mustSel(t, in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ParseSelector(%q) = %+v, want %+v", in, got, want)
		}
	}
	for _, bad := range []string{``, `   `, `button "a" "b"`, `"open`, `button "x" #0`, `bogus|button "x"`, `button "Save" now`} {
		if _, err := ParseSelector(bad); code(t, err) != "invalid_argument" {
			t.Errorf("ParseSelector(%q) = %v, want invalid_argument", bad, err)
		}
	}
	if got := mustSel(t, `checked checkbox "Wrap" #2 in sheet "S"`).String(); got != `checked checkbox "Wrap" #2 in sheet "S"` {
		t.Errorf("String: %q", got)
	}
}

func TestFindExactBeforeContained(t *testing.T) {
	s := snap(t, textEdit, build(
		spec{depth: 0, role: "menuitem", name: "Save As…"},
		spec{depth: 0, role: "menuitem", name: "Save"},
		spec{depth: 0, role: "button", name: "save"},
		spec{depth: 0, role: "button", name: ""},
		spec{depth: 0, role: "tab", name: "Saved"},
	))
	find := func(q string) []int {
		m, err := Find(s, mustSel(t, q))
		if err != nil {
			return nil
		}
		return m.Index
	}
	if got := find(`"save"`); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Errorf("save: %v", got)
	}
	if got := find(`"SAVE"`); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Errorf("SAVE: %v", got)
	}
	// An action takes the name as given before the case-folded one.
	twins := snap(t, textEdit, build(spec{depth: 0, role: "button", name: "save"}, spec{depth: 0, role: "button", name: "Save"}))
	if i, tier, err := Resolve(twins, mustSel(t, `"Save"`)); err != nil || i != 1 || tier != TierExact {
		t.Errorf("exact Save beats case-folded: %d %q %v", i, tier, err)
	}
	if got := find(`"save as"`); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("save as: %v", got)
	}
	if got := find(`button "save"`); !reflect.DeepEqual(got, []int{2}) {
		t.Errorf("button save: %v", got)
	}
	if got := find(`tab|button "save"`); !reflect.DeepEqual(got, []int{2}) {
		t.Errorf("tab|button save: %v", got)
	}
	if got := find(`tab "sav"`); !reflect.DeepEqual(got, []int{4}) {
		t.Errorf("contained: %v", got)
	}
	if got := find(`"..."`); got != nil {
		t.Errorf("punctuation-only query matched %v", got)
	}
	if got := find(`button "_"`); got != nil {
		t.Errorf("underscore query matched %v", got)
	}
	m, err := Find(s, mustSel(t, `"save"`))
	if err != nil || m.Tier != TierName {
		t.Errorf("tier %q %v", m.Tier, err)
	}
	_, err = Find(s, mustSel(t, `"Quit"`))
	if code(t, err) != "target_not_found" {
		t.Errorf("not found: %v", err)
	}
}

func TestResolvePrefersShowing(t *testing.T) {
	nodes := build(
		spec{depth: 0, role: "menubar"},
		spec{depth: 1, role: "menubaritem", name: "Format"},
		spec{depth: 2, role: "menu"},
		spec{depth: 3, role: "menuitem", name: "Bold"},
		spec{depth: 0, role: "toolbar"},
		spec{depth: 1, role: "button", name: "Bold"}, // no frame: still showing
		spec{depth: 1, role: "button", name: "Italic", states: []string{"hidden"}},
		spec{depth: 1, role: "button", name: "Underline", bounds: box(0, 0, 0, 0)},
	)
	s := snap(t, textEdit, nodes)
	i, _, err := Resolve(s, mustSel(t, "the Bold button"))
	if err != nil || i != 5 {
		t.Errorf("Bold button: %d %v", i, err)
	}
	// A bare name: the showing button wins over the hidden menu item.
	if i, _, err := Resolve(s, mustSel(t, `"Bold"`)); err != nil || i != 5 {
		t.Errorf(`"Bold": %d %v`, i, err)
	}
	// A hidden element is skipped even on an exact name.
	if _, _, err := Resolve(s, mustSel(t, `button "Italic"`)); code(t, err) != "target_not_found" {
		t.Errorf("hidden Italic: %v", err)
	}
	if _, _, err := Resolve(s, mustSel(t, `button "Underline"`)); code(t, err) != "target_not_found" {
		t.Errorf("zero-size Underline: %v", err)
	}
	// Find (a view, not an action) still lists it.
	if m, err := Find(s, mustSel(t, `button "Italic"`)); err != nil || len(m.Index) != 1 {
		t.Errorf("find hidden: %v %v", m, err)
	}
	// With nothing showing of that name, the hidden menu item is taken.
	s2 := snap(t, textEdit, build(nodesSpec(nodes[:4])...))
	if i, _, err := Resolve(s2, mustSel(t, `"Bold"`)); err != nil || i != 3 {
		t.Errorf("menu item fallback: %d %v", i, err)
	}
	// A menu item the backend marks showing (its menu is open) counts.
	open := build(
		spec{depth: 0, role: "menuitem", name: "Bold", states: []string{"showing"}},
		spec{depth: 0, role: "button", name: "Bold"},
	)
	if _, _, err := Resolve(snap(t, textEdit, open), mustSel(t, `"Bold"`)); code(t, err) != "target_ambiguous" {
		t.Errorf("open menu and button: %v", err)
	}
}

// nodesSpec turns nodes back into specs.
func nodesSpec(nodes []RawNode) []spec {
	out := make([]spec, len(nodes))
	for i, n := range nodes {
		out[i] = spec{depth: n.Depth, role: n.Role, name: n.Name, desc: n.Desc, states: n.States, bounds: n.Bounds}
	}
	return out
}

func TestResolveListItems(t *testing.T) {
	nodes := build(
		spec{depth: 0, role: "menubar"},
		spec{depth: 1, role: "menubaritem", name: "View"},
		spec{depth: 2, role: "menu"},
		spec{depth: 3, role: "menuitem", name: "Two Columns"},
		spec{depth: 0, role: "list"},
		spec{depth: 1, role: "listitem", name: "Two Columns", bounds: box(10, 10, 80, 60)},
		spec{depth: 1, role: "listitem", name: "Three Columns", bounds: box(100, 10, 80, 60)},
		spec{depth: 0, role: "table"},
		spec{depth: 1, role: "row"},
		spec{depth: 2, role: "cell", name: "Invoice 42"},
		spec{depth: 0, role: "listitem", name: "Weekly report summary"}, // outside any list
	)
	s := snap(t, textEdit, nodes)
	if i, _, err := Resolve(s, mustSel(t, `"Two Columns"`)); err != nil || i != 5 {
		t.Errorf("drawn tile over hidden menu entry: %d %v", i, err)
	}
	if i, _, err := Resolve(s, mustSel(t, `cell "Invoice 42"`)); err != nil || i != 9 {
		t.Errorf("table cell: %d %v", i, err)
	}
	if i, _, err := Resolve(s, mustSel(t, `"invoice"`)); err != nil || i != 9 {
		t.Errorf("cell by fragment inside a table: %d %v", i, err)
	}
	// An item outside any list is not taken for a loose description.
	if _, _, err := Resolve(s, mustSel(t, `"report"`)); code(t, err) != "target_not_found" {
		t.Errorf("loose word took an item outside a list: %v", err)
	}
	if i, _, err := Resolve(s, mustSel(t, `"Weekly report summary"`)); err != nil || i != 10 {
		t.Errorf("full name of an item outside a list: %d %v", i, err)
	}
}

func TestUnnamedControlInheritsLabel(t *testing.T) {
	nodes := build(
		spec{depth: 0, role: "tablist"},
		spec{depth: 1, role: "group", name: "Properties"},
		spec{depth: 2, role: "button"}, // icon-only, alone in its group
		spec{depth: 1, role: "group", desc: "Gallery"},
		spec{depth: 2, role: "group"},
		spec{depth: 3, role: "button"}, // two levels of single-child wrappers
		spec{depth: 1, role: "group", name: "Shared"},
		spec{depth: 2, role: "button"}, // two buttons: no label to inherit
		spec{depth: 2, role: "button"},
		spec{depth: 1, role: "button", desc: "Navigator"}, // its own description
	)
	s := snap(t, textEdit, nodes)
	for q, want := range map[string]int{
		`button "Properties"`: 2,
		`button "Gallery"`:    5,
		`button "Navigator"`:  9,
	} {
		i, tier, err := Resolve(s, mustSel(t, q))
		if err != nil || i != want || tier != TierLabel {
			t.Errorf("%s: %d %q %v", q, i, tier, err)
		}
	}
	if _, _, err := Resolve(s, mustSel(t, `button "Shared"`)); code(t, err) != "target_not_found" {
		t.Errorf("shared group label inherited: %v", err)
	}
	// A name beats a description.
	s = snap(t, textEdit, build(
		spec{depth: 0, role: "button", desc: "Print"},
		spec{depth: 0, role: "button", name: "Print"},
	))
	if i, _, err := Resolve(s, mustSel(t, `"Print"`)); err != nil || i != 1 {
		t.Errorf("name before description: %d %v", i, err)
	}
}

func TestResolveAmbiguousAndNotFound(t *testing.T) {
	s := snap(t, textEdit, build(
		spec{depth: 0, role: "button", name: "OK"},
		spec{depth: 0, role: "button", name: "OK"},
		spec{depth: 0, role: "button", name: "Save…"},
		spec{depth: 0, role: "textbox", name: "Password", states: []string{"protected"}},
		spec{depth: 0, role: "textbox", name: "User"},
		spec{depth: 0, role: "checkbox", name: "Remember", states: []string{"checked"}},
	))
	_, _, err := Resolve(s, mustSel(t, `button "OK"`))
	if code(t, err) != "target_ambiguous" || !strings.Contains(hint(err), `e2 button "OK"`) || !strings.Contains(hint(err), `e3 button "OK"`) {
		t.Errorf("ambiguous: %v / %s", err, hint(err))
	}
	if i, _, err := Resolve(s, mustSel(t, `button "OK" #2`)); err != nil || i != 1 {
		t.Errorf("#2: %d %v", i, err)
	}
	if _, _, err := Resolve(s, mustSel(t, `button "OK" #3`)); code(t, err) != "target_not_found" {
		t.Errorf("#3: %v", err)
	}
	_, _, err = Resolve(s, mustSel(t, `button "Save as"`))
	if code(t, err) != "target_not_found" || !strings.Contains(hint(err), `closest: e4 button "Save…"`) {
		t.Errorf("closest: %v / %s", err, hint(err))
	}
	if i, _, err := Resolve(s, mustSel(t, `secure textbox`)); err != nil || i != 3 {
		t.Errorf("secure textbox: %d %v", i, err)
	}
	if i, _, err := Resolve(s, mustSel(t, `checked checkbox`)); err != nil || i != 5 {
		t.Errorf("checked checkbox: %d %v", i, err)
	}
	if _, _, err := Resolve(s, mustSel(t, `unchecked checkbox`)); code(t, err) != "target_not_found" {
		t.Errorf("unchecked checkbox: %v", err)
	}
}

func TestSelectorScope(t *testing.T) {
	sheet := RawWindow{Key: "ax:1", Title: "Export", Role: "sheet"}
	nodes := build(
		spec{depth: 0, role: "sheet", name: "Export"},
		spec{depth: 1, role: "group", name: "Options"},
		spec{depth: 2, role: "textbox", name: "Name"},
		spec{depth: 1, role: "group", name: "Target"},
		spec{depth: 2, role: "textbox", name: "Name"},
	)
	s := snap(t, sheet, nodes)
	if _, _, err := Resolve(s, mustSel(t, `textbox "Name"`)); code(t, err) != "target_ambiguous" {
		t.Errorf("unscoped: %v", err)
	}
	if i, _, err := Resolve(s, mustSel(t, `textbox "Name" in group "Target"`)); err != nil || i != 4 {
		t.Errorf("in group: %d %v", i, err)
	}
	// The window itself as the scope.
	if _, _, err := Resolve(s, mustSel(t, `textbox "Name" in sheet "Export"`)); code(t, err) != "target_ambiguous" {
		t.Errorf("in the sheet itself: %v", err)
	}
	if _, _, err := Resolve(s, mustSel(t, `textbox "Name" in group "Missing"`)); code(t, err) != "target_not_found" {
		t.Errorf("missing scope: %v", err)
	}
}
