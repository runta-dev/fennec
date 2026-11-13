package ego

import (
	"strings"
	"testing"
)

func trees(t *testing.T, root *spec, url string) (*AXTree, *DOM) {
	t.Helper()
	p := &fakePage{}
	p.load(root)
	ax, err := ParseAXTree(axJSON(root, url))
	if err != nil {
		t.Fatal(err)
	}
	dom, err := ParseDOM(domJSON(root))
	if err != nil {
		t.Fatal(err)
	}
	return ax, dom
}

// signIn is a synthetic sign-in page.
func signIn() *spec {
	return n("RootWebArea", "Sign in",
		n("generic", "",
			n("generic", "",
				n("heading", "Sign in", n("StaticText", "Sign in", n("InlineTextBox", "Sign in"))).with("level", 1),
			),
			n("generic", "",
				n("LabelText", "", n("StaticText", "Email ")),
				n("textbox", "Email", n("generic", "", n("StaticText", "a@b.c"))).from("relatedElement").val("a@b.c").with("settable", true).with("focusable", true).with("focused", true).id("email"),
			),
			n("combobox", "Language", n("MenuListPopup", "", n("option", "English").with("selected", true))).from("attribute").val("English").with("expanded", false).id("lang"),
			n("button", "Close dialog", n("StaticText", "X")).from("attribute").with("focusable", true),
			n("button", "Continue", n("StaticText", "Continue")).with("focusable", true).id("go"),
			n("checkbox", "Remember me").with("checked", true),
			n("link", "Sign up", n("StaticText", "Sign up")).with("url", "https://example.test/sign-up"),
			n("link", "Help", n("image", "help").from("attribute"), n("StaticText", "Help")).with("url", "https://example.test/help"),
			n("StaticText", "   "),
			n("list", "", n("listitem", "", n("ListMarker", "• "), n("StaticText", "One"))),
		),
	)
}

// The full state, byte for byte.
func TestStateFull(t *testing.T) {
	ax, dom := trees(t, signIn(), "https://example.test/login")
	got := NewMemory().Read("7", "Sign in", "https://example.test/login", ax, DOMIDs(dom), true)
	want := "Browser tab: 7, Title: \"Sign in\", URL: \"https://example.test/login\".\n" +
		"0 AXWebArea Sign in, URL: example.test/login\n" +
		"\t1 container\n" +
		"\t\t2 heading Sign in, Value: 1\n" +
		"\t\t\t3 text Sign in\n" +
		"\t\t4 container\n" +
		"\t\t\t5 text Email \n" +
		"\t\t\t6 text field (settable) Email, Value: a@b.c, ID: email\n" +
		"\t\t7 pop up button (collapsed) Description: Language, Value: English, ID: lang, Secondary Actions: Expand\n" +
		"\t\t8 button Description: Close dialog\n" +
		"\t\t\t9 text X\n" +
		"\t\t10 button Continue, ID: go\n" +
		"\t\t\t11 text Continue\n" +
		"\t\t12 checkbox Remember me, Value: 1\n" +
		"\t\t13 link Description: Sign up, Value: example.test/sign-up\n" +
		"\t\t14 link Help\n" +
		"\t\t\t15 image help\n" +
		"\t\t\t16 text Help\n" +
		"\t\t17 content list\n" +
		"\t\t\t18 container\n" +
		"\t\t\t\t19 AXListMarker • \n" +
		"\t\t\t\t20 text One\n" +
		"\n" +
		"The focused UI element is 6 text field (settable) Email, Value: a@b.c, ID: email"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// The diff: stable ids, fresh ids above every used one, removed ranges,
// ~ and + with their depth, the focused line after a blank line.
func TestStateDiff(t *testing.T) {
	m := NewMemory()
	root := signIn()
	ax, dom := trees(t, root, "https://example.test/login")
	m.Read("7", "Sign in", "https://example.test/login", ax, DOMIDs(dom), true)
	// Type into the field, drop the remember box and the list, add a note.
	box := root.kids[0]
	field := box.kids[1].kids[1]
	field.value = "a@b.cd"
	field.kids[0].kids[0].name = "a@b.cd"
	box.kids = append(box.kids[:5], box.kids[6:]...) // remove the checkbox
	box.kids = box.kids[:len(box.kids)-1]            // remove the list
	note := n("StaticText", "Check your email")
	note.backend = 500
	box.kids = append(box.kids, note)
	// Surviving nodes keep their backend ids, as Chromium's do.
	ax2, err := ParseAXTree(axJSON(root, "https://example.test/login"))
	if err != nil {
		t.Fatal(err)
	}
	dom2, _ := ParseDOM(domJSON(root))
	got := m.Read("7", "Sign in", "https://example.test/login", ax2, DOMIDs(dom2), true)
	if !strings.HasPrefix(got, "Browser tab: 7, Title: \"Sign in\", URL: \"https://example.test/login\".\nThe following is a diff from the previous accessibility tree with ~ and + representing changed and added elements, respectively. Removed elements are summarized by ID range.\n") {
		t.Fatalf("header:\n%s", got)
	}
	if !strings.Contains(got, "\nRemoved element IDs: 12, 17-20\n~\t\t\t6 text field (settable) Email, Value: a@b.cd, ID: email\n+\t\t21 text Check your email\n") {
		t.Errorf("diff:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n\nThe focused UI element is 6 text field (settable) Email, Value: a@b.cd, ID: email") {
		t.Errorf("focus line:\n%s", got)
	}
}

// A hand-made pair of reads checks the exact diff text.
func TestStateDiffExact(t *testing.T) {
	m := NewMemory()
	mk := func(items ...*spec) *AXTree {
		root := &spec{role: "RootWebArea", name: "List", backend: 1, kids: items}
		ax, err := ParseAXTree(axJSON(root, "https://example.test/"))
		if err != nil {
			t.Fatal(err)
		}
		return ax
	}
	item := func(b int, name string) *spec {
		return &spec{role: "button", name: name, backend: b, props: map[string]any{"focusable": true}}
	}
	first := mk(item(10, "A"), item(11, "B"), item(12, "C"), item(13, "D"), item(14, "E"), item(15, "F"))
	m.Read("3", "List", "https://example.test/", first, nil, true)
	second := mk(item(10, "A"), item(13, "D2"), item(14, "E"), item(15, "F"), item(20, "G"))
	got := m.Read("3", "List", "https://example.test/", second, nil, true)
	want := "Browser tab: 3, Title: \"List\", URL: \"https://example.test/\".\n" +
		"The following is a diff from the previous accessibility tree with ~ and + representing changed and added elements, respectively. Removed elements are summarized by ID range.\n" +
		"Removed element IDs: 2-3\n" +
		"~\t4 button D2\n" +
		"+\t7 button G\n" +
		"\n" +
		"The focused UI element is 0 AXWebArea List, URL: example.test/"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if e, ok := m.Element(7); !ok || e.Backend != 20 || e.Name != "G" {
		t.Errorf("element 7: %+v", e)
	}
	if _, ok := m.Element(2); ok {
		t.Error("a removed id still resolves")
	}
	// Nothing changed: one line.
	if got := m.Read("3", "List", "https://example.test/", second, nil, true); got != "Browser tab: 3, Title: \"List\", URL: \"https://example.test/\".\nThe accessibility tree has not changed since the previous read." {
		t.Errorf("unchanged: %q", got)
	}
	// disableDiffing: the whole tree with the same ids.
	if got := m.Read("3", "List", "https://example.test/", second, nil, false); !strings.Contains(got, "\n\t4 button D2\n\t5 button E\n\t6 button F\n\t7 button G\n") {
		t.Errorf("full:\n%s", got)
	}
	// Another URL: the whole tree, ids kept.
	if got := m.Read("3", "List", "https://example.test/?p=2", second, nil, true); strings.Contains(got, "diff") || !strings.Contains(got, "\t7 button G") {
		t.Errorf("url change:\n%s", got)
	}
	// A new document: ids start again.
	third := &spec{role: "RootWebArea", name: "Other", backend: 99, kids: []*spec{item(100, "X")}}
	ax3, _ := ParseAXTree(axJSON(third, "https://example.test/other"))
	if got := m.Read("3", "Other", "https://example.test/other", ax3, nil, true); !strings.Contains(got, "\n0 AXWebArea Other, URL: example.test/other\n\t1 button X\n") {
		t.Errorf("new document:\n%s", got)
	}
	// More than half the lines new: the whole tree.
	big := []*spec{item(100, "X")}
	for i := 0; i < 6; i++ {
		big = append(big, item(200+i, "N"))
	}
	third.kids = big
	ax4, _ := ParseAXTree(axJSON(third, "https://example.test/other"))
	if got := m.Read("3", "Other", "https://example.test/other", ax4, nil, true); strings.Contains(got, "diff") {
		t.Errorf("large change gives a diff:\n%s", got)
	}
}

func TestRoleWords(t *testing.T) {
	cases := []struct {
		node *spec
		want string
	}{
		{n("textbox", "Notes").with("multiline", true).with("settable", true), "text entry area (settable) Notes"},
		{n("searchbox", "Find").from("placeholder").with("settable", true), "search text field (settable) Find"},
		{n("combobox", "Pick").with("settable", true).with("expanded", true), "combo box (expanded, settable) Pick, Secondary Actions: Collapse"},
		{n("button", "Menu").with("hasPopup", "menu").with("expanded", false), "pop up button (collapsed) Menu, Secondary Actions: Expand"},
		{n("button", "Bold").with("pressed", "false"), "toggle button Bold"},
		{n("tab", "T1").with("selected", true), "tab (selected) T1"},
		{n("tab", "T2").with("selected", false), "tab (selectable) T2"},
		{n("button", "Off").with("disabled", true), "button (disabled) Off"},
		{n("radio", "Yes").with("checked", "mixed"), "radio button Yes, Value: 2"},
		{n("navigation", "Main"), "container Main"},
		{n("link", "").with("url", "javascript:void(0);"), "link javascript:void(0);"},
		{n("slider", "Volume").val("40").with("settable", true), "slider (settable) Volume, Value: 40"},
		{n("textbox", "Bio").val("line one\nline two"), "text field Bio, Value: line one\\nline two"},
	}
	for _, c := range cases {
		root := n("RootWebArea", "T", c.node)
		ax, _ := trees(t, root, "https://x.test/")
		got := NewMemory().Read("1", "T", "https://x.test/", ax, nil, false)
		if !strings.Contains(got, "\n\t1 "+c.want+"\n") {
			t.Errorf("%s: got\n%s", c.want, got)
		}
	}
}

func TestShortURL(t *testing.T) {
	if got := shortURL("http://127.0.0.1:5180/?v=1"); got != "127.0.0.1:5180/?v=1" {
		t.Error(got)
	}
	if got := shortURL("chrome-error://chromewebdata/"); got != "chrome-error://chromewebdata/" {
		t.Error(got)
	}
	long := "data:text/html;charset=utf-8," + strings.Repeat("x", 100)
	if got := shortURL(long); got != "data:text/html;charset=utf-8,…" {
		t.Error(got)
	}
}

func TestAriaSnapshot(t *testing.T) {
	ax, _ := trees(t, signIn(), "https://example.test/login")
	got := AriaSnapshot(ax)
	want := `- generic:
  - heading "Sign in" [level=1]
  - generic:
    - generic: Email
    - textbox "Email": a@b.c
  - combobox "Language": English
  - button "Close dialog": X
  - button "Continue"
  - checkbox "Remember me" [checked]
  - link "Sign up":
    - /url: "https://example.test/sign-up"
  - link "Help":
    - img "help"
    - text: Help
    - /url: "https://example.test/help"
  - list:
    - listitem: One`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if yamlText("534") != `"534"` || yamlText("Active users") != "Active users" || yamlText("a: b") != `"a: b"` || yamlText("true") != `"true"` {
		t.Error("yaml quoting")
	}
}
