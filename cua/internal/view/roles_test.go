package view

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeRole(t *testing.T) {
	for _, c := range []struct {
		role, sub, desc, want string
	}{
		{"AXButton", "", "", "button"},
		{"AXCheckBox", "", "", "checkbox"},
		{"AXCheckBox", "AXSwitch", "", "switch"},
		{"AXCheckBox", "AXToggle", "", "button"},
		{"AXRadioButton", "", "", "radio"},
		{"AXRadioButton", "AXTabButton", "", "tab"},
		{"AXTextField", "", "", "textbox"},
		{"AXTextField", "AXSecureTextField", "", "textbox"},
		{"AXTextField", "AXSearchField", "", "textbox"},
		{"AXTextArea", "", "", "textbox"},
		{"AXComboBox", "", "", "combobox"},
		{"AXPopUpButton", "", "", "combobox"},
		{"AXSlider", "", "", "slider"},
		{"AXIncrementor", "", "", "spinbutton"},
		{"AXMenuBar", "", "", "menubar"},
		{"AXMenuBarItem", "", "", "menubaritem"},
		{"AXMenu", "", "", "menu"},
		{"AXMenuItem", "", "", "menuitem"},
		{"AXTabGroup", "", "", "tablist"},
		{"AXList", "", "", "list"},
		{"AXTable", "", "", "table"},
		{"AXRow", "", "", "row"},
		{"AXRow", "AXOutlineRow", "", "treeitem"},
		{"AXCell", "", "", "cell"},
		{"AXOutline", "", "", "tree"},
		{"AXLink", "", "", "link"},
		{"AXImage", "", "", "image"},
		{"AXStaticText", "", "", "text"},
		{"AXHeading", "", "", "heading"},
		{"AXGroup", "", "", "group"},
		{"AXToolbar", "", "", "toolbar"},
		{"AXWindow", "", "", "window"},
		{"AXWindow", "AXDialog", "", "dialog"},
		{"AXWindow", "AXFloatingWindow", "", "panel"},
		{"AXSheet", "", "", "sheet"},
		{"AXScrollArea", "", "", "scrollarea"},
		{"AXProgressIndicator", "", "", "progressbar"},
		{"AXWebArea", "", "", "document"},
		{"AXGroup", "AXApplicationDialog", "", "dialog"},
		{"AXGroup", "AXTabPanel", "", "tabpanel"},
		{"AXGroup", "AXLandmarkNavigation", "", "navigation"},
		{"AXGroup", "", "list item", "listitem"},
		{"AXGroup", "", "Tab Panel", "tabpanel"},
		{"AXButton", "", "heading", "button"}, // a description never overrides a control
		{"AXValueIndicator", "", "", "valueindicator"},
		{"AXSomethingNew", "", "", "somethingnew"},
		{"", "", "", "generic"},
	} {
		if got := NormalizeRole(c.role, c.sub, c.desc); got != c.want {
			t.Errorf("NormalizeRole(%q, %q, %q) = %q, want %q", c.role, c.sub, c.desc, got, c.want)
		}
	}
	if WindowRole("", true) != "sheet" || WindowRole("AXSystemDialog", false) != "dialog" || WindowRole("AXStandardWindow", false) != "window" {
		t.Error("WindowRole")
	}
}

func TestStates(t *testing.T) {
	for _, c := range []struct {
		role, sub string
		value     any
		f, s, e   bool
		enabled   bool
		want      []string
	}{
		{"AXCheckBox", "", 1, false, false, false, true, []string{"checked"}},
		{"AXCheckBox", "", float64(2), false, false, false, true, []string{"mixed"}},
		{"AXCheckBox", "", 0, true, false, false, true, []string{"focused"}},
		{"AXCheckBox", "AXSwitch", true, false, false, false, true, []string{"checked"}},
		{"AXRadioButton", "", int64(1), false, true, false, true, []string{"selected", "checked"}},
		{"AXCheckBox", "AXToggle", 1, false, false, false, true, []string{"pressed"}},
		{"AXButton", "", 1, false, false, false, true, nil},
		{"AXTextField", "AXSecureTextField", "secret", true, false, false, true, []string{"focused", "protected"}},
		{"AXTextField", "", nil, false, false, false, false, []string{"disabled"}},
		{"AXRow", "AXOutlineRow", nil, false, true, true, true, []string{"selected", "expanded"}},
	} {
		got := States(c.role, c.sub, c.value, c.f, c.s, c.e, c.enabled)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("States(%s/%s, %v) = %v, want %v", c.role, c.sub, c.value, got, c.want)
		}
	}
}

func TestShowing(t *testing.T) {
	for _, c := range []struct {
		n    RawNode
		want bool
	}{
		{RawNode{Role: "button"}, true},
		{RawNode{Role: "button", Bounds: box(0, 0, 10, 10)}, true},
		{RawNode{Role: "button", Bounds: box(0, 0, 0, 10)}, false},
		{RawNode{Role: "button", States: []string{"hidden"}}, false},
		{RawNode{Role: "menuitem"}, false},
		{RawNode{Role: "menuitem", States: []string{"showing"}}, true},
		{RawNode{Role: "menubaritem"}, true},
	} {
		if got := Showing(c.n); got != c.want {
			t.Errorf("Showing(%+v) = %v", c.n, got)
		}
	}
}

// A live menu item reports its check mark only as AXMenuItemMarkChar; read
// as AXValue alone, View > Show Status Bar never read as on.
func TestMenuMarkStates(t *testing.T) {
	for mark, want := range map[string]string{"": "", "✓": "checked", "•": "checked", "-": "mixed"} {
		got := strings.Join(MenuMarkStates(mark), ",")
		if got != want {
			t.Errorf("mark %q: %q, want %q", mark, got, want)
		}
	}
}

// A slider refuses a string value; the set writes the number it reads.
func TestNumberOf(t *testing.T) {
	for in, want := range map[string]float64{"14": 14, " 14 pt": 14, "-2.5": -2.5, "30%": 30} {
		if got, ok := NumberOf(in); !ok || got != want {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	if _, ok := NumberOf("Blue"); ok {
		t.Error("Blue is no number")
	}
}
