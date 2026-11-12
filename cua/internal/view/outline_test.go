package view

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var textEdit = RawWindow{Key: "cg:7", Title: "Untitled", App: "TextEdit", Role: "window", PID: 70, Active: true}

func TestOutlineLineFormat(t *testing.T) {
	s := snap(t, textEdit, build(
		spec{depth: 0, role: "window", name: "Untitled"},
		spec{depth: 1, role: "toolbar"},
		spec{depth: 2, role: "button", name: "Save", states: []string{"disabled", "showing"}, keys: "cmd+s"},
		spec{depth: 2, role: "checkbox", name: "Wrap", states: []string{"checked"}},
		spec{depth: 1, role: "textbox", name: "Name", states: []string{"focused"}, value: str("Alice")},
		spec{depth: 1, role: "textbox", name: "Notes", value: str(strings.Repeat("x", 100))},
	))
	got := RenderOutline(s, OutlineOptions{}).Text
	want := strings.Join([]string{
		`w1 window "Untitled" (TextEdit) [active]`,
		`  e2 toolbar`,
		`    e3 button "Save" [disabled] key=cmd+s`,
		`    e4 checkbox "Wrap" [checked]`,
		`  e5 textbox "Name" [focused]: "Alice"`,
		`  e6 textbox "Notes": "` + strings.Repeat("x", 80) + `"… (100 chars)`,
	}, "\n")
	if got != want {
		t.Errorf("outline:\n%s\nwant:\n%s", got, want)
	}
	// Without a window root, depth 0 nodes sit one level under the window.
	s = snap(t, textEdit, build(spec{depth: 0, role: "button", name: "OK"}))
	if got := RenderOutline(s, OutlineOptions{}).Text; got != "w1 window \"Untitled\" (TextEdit) [active]\n  e2 button \"OK\"" {
		t.Errorf("rootless outline: %q", got)
	}
	// Window line states.
	w := RawWindow{Title: "Old", Role: "sheet", Hidden: "minimized", Space: "other", Stale: true}
	if got := WindowLine("w4", w); got != `w4 sheet "Old" [minimized other-space stale]` {
		t.Errorf("window line: %q", got)
	}
}

func TestOutlineEscaping(t *testing.T) {
	forged := "Pay\"\n  e99 button \"Confirm"
	s := snap(t, RawWindow{Key: "k", Title: "Bank\n  w9 window \"Evil\"", Role: "window"}, build(
		spec{depth: 0, role: "text", name: forged, value: str("v\"\nlue\u2028x")},
		spec{depth: 0, role: "link", name: strings.Repeat("y", 200)},
	))
	out := RenderOutline(s, OutlineOptions{}).Text
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("screen text forged lines:\n%s", out)
	}
	if !strings.Contains(lines[0], `"Bank\n  w9 window \"Evil\""`) {
		t.Errorf("title not escaped: %q", lines[0])
	}
	if !strings.Contains(lines[1], `"Pay\"\n  e99 button \"Confirm"`) || !strings.Contains(lines[1], `: "v\"\nlue\u2028x"`) {
		t.Errorf("name or value not escaped: %q", lines[1])
	}
	// Every line has balanced, unescaped quotes.
	for _, l := range lines {
		if n := strings.Count(strings.ReplaceAll(l, `\"`, ""), `"`); n%2 != 0 {
			t.Errorf("unbalanced quotes in %q", l)
		}
	}
	// Labels are capped at 80 characters, values at 60 in vision lists.
	if !strings.Contains(lines[2], `"`+strings.Repeat("y", 80)+`"…`) {
		t.Errorf("long label not capped: %q", lines[2])
	}
	v := VisionLine(3, "textbox", forged, str(strings.Repeat("z", 70)))
	if strings.Contains(v, "\n") || !strings.HasSuffix(v, `: "`+strings.Repeat("z", 60)+`"…`) {
		t.Errorf("vision line: %q", v)
	}
}

func menuTree() []RawNode {
	return build(
		spec{depth: 0, role: "window", name: "Doc"},
		spec{depth: 1, role: "button", name: "Save"},
		spec{depth: 1, role: "menubar"},
		spec{depth: 2, role: "menubaritem", name: "File"},
		spec{depth: 3, role: "menu"},
		spec{depth: 4, role: "menuitem", name: "Export…", keys: "cmd+e"},
		spec{depth: 1, role: "link", name: "Help", url: "https://example.com/help"},
	)
}

func TestMenusOnlyWhenAsked(t *testing.T) {
	s := snap(t, textEdit, menuTree())
	without := RenderOutline(s, OutlineOptions{}).Text
	if strings.Contains(without, "menubar") || strings.Contains(without, "Export") {
		t.Errorf("menus shown unasked:\n%s", without)
	}
	with := RenderOutline(s, OutlineOptions{Menus: true}).Text
	if !strings.Contains(with, `menuitem "Export…" key=cmd+e`) {
		t.Errorf("menus missing when asked:\n%s", with)
	}
	if n := Nodes(s, nil, false, false); len(n) != 2 {
		t.Errorf("nodes without menus: %+v", n)
	}

	// Through observe: the walk takes menus only when asked, find always.
	src := &fakeSource{windows: []RawWindow{textEdit}, trees: map[string]Tree{textEdit.Key: {Nodes: menuTree()}}}
	o := &Observer{Source: src, Refs: NewRegistry(0)}
	res, err := o.Observe(ctx(), obsReq{}.req())
	if err != nil || strings.Contains(res.Outline, "File") || src.treeCalls[0].Menus {
		t.Fatalf("plain observe: %v %+v %q", err, src.treeCalls, res.Outline)
	}
	res, err = o.Observe(ctx(), obsReq{menus: true}.req())
	if err != nil || !strings.Contains(res.Outline, `menubaritem "File"`) || !src.treeCalls[1].Menus {
		t.Fatalf("menus observe: %v %q", err, res.Outline)
	}
	res, err = o.Observe(ctx(), obsReq{find: `menuitem "Export"`}.req())
	if err != nil || !src.treeCalls[2].Menus || !strings.Contains(res.Outline, "Export…") {
		t.Fatalf("find searches the menu bar: %v %+v", err, res)
	}
	if res.Resolved == nil || res.Resolved.How != "name" {
		t.Errorf("find resolution: %+v", res.Resolved)
	}
}

func TestLinkUrlsWhenAsked(t *testing.T) {
	s := snap(t, textEdit, menuTree())
	if got := RenderOutline(s, OutlineOptions{}).Text; strings.Contains(got, "example.com") {
		t.Errorf("url unasked: %s", got)
	}
	if got := RenderOutline(s, OutlineOptions{URLs: true}).Text; !strings.Contains(got, `link "Help" url="https://example.com/help"`) {
		t.Errorf("url asked: %s", got)
	}
	src := &fakeSource{
		windows: []RawWindow{textEdit},
		trees:   map[string]Tree{textEdit.Key: {Nodes: menuTree()}},
	}
	o := &Observer{Source: src, Refs: NewRegistry(0)}
	res, err := o.Observe(ctx(), obsReq{find: `link "Help"`, nodes: true}.req())
	if err != nil || len(res.Nodes) != 1 || res.Nodes[0].URL != "" {
		t.Fatalf("find without urls: %v %+v", err, res)
	}
	o.URLs = true
	res, err = o.Observe(ctx(), obsReq{find: `link "Help"`, nodes: true}.req())
	if err != nil || len(res.Nodes) != 1 || res.Nodes[0].URL != "https://example.com/help" {
		t.Fatalf("find with urls: %v %+v", err, res)
	}
	if !src.treeCalls[1].URLs {
		t.Errorf("walk not asked for urls: %+v", src.treeCalls)
	}
	// An element read always carries the link target.
	link := s.Tree.Nodes[6]
	n, _ := ReadNode(link, "e7", false, false, false, false)
	if n.URL != "https://example.com/help" {
		t.Errorf("read without url: %+v", n)
	}
}

func TestTruncationNoteKinds(t *testing.T) {
	nodes := nodesN(20)
	s := snap(t, RawWindow{Key: "k", Title: "T"}, nodes)
	full := RenderOutline(s, OutlineOptions{Budget: 1 << 20})
	if full.Cut || len(full.Notes) != 0 {
		t.Fatalf("uncut outline: %+v", full)
	}
	size := len([]rune(full.Text))
	// Exactly at the budget: no note.
	at := RenderOutline(s, OutlineOptions{Budget: size})
	if at.Cut || len(at.Notes) != 0 || at.Text != full.Text {
		t.Errorf("at the budget: %+v", at.Notes)
	}
	// One character short: the budget note.
	short := RenderOutline(s, OutlineOptions{Budget: size - 1})
	if !short.Cut || len(short.Notes) != 1 || !strings.Contains(short.Notes[0], fmt.Sprintf("%d-character budget", size-1)) ||
		!strings.Contains(short.Notes[0], "find or scope element") {
		t.Errorf("budget note: %+v", short.Notes)
	}
	if len([]rune(short.Text)) > size-1 {
		t.Errorf("over budget: %d", len([]rune(short.Text)))
	}
	// The node cap: its own note.
	s.Tree.Truncated = true
	capped := RenderOutline(s, OutlineOptions{Budget: 1 << 20})
	if !capped.Cut || len(capped.Notes) != 1 || !strings.Contains(capped.Notes[0], "2500 elements") {
		t.Errorf("node cap note: %+v", capped.Notes)
	}
	both := RenderOutline(s, OutlineOptions{Budget: size - 1})
	if len(both.Notes) != 2 {
		t.Errorf("both notes: %+v", both.Notes)
	}
}

func TestNodeJSONOmitsEmpty(t *testing.T) {
	n := NodeOf(RawNode{Role: "button", States: []string{"showing"}}, "e1", "", true)
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"ref":"e1","role":"button","depth":0}` {
		t.Errorf("compact node: %s", b)
	}
	n = NodeOf(RawNode{Role: "textbox", Name: "N", Value: str(""), States: []string{"focused"}, Bounds: box(1, 2, 3, 4)}, "e2", "e1", true)
	b, _ = json.Marshal(n)
	if string(b) != `{"ref":"e2","role":"textbox","name":"N","value":"","states":["focused"],"bounds":[1,2,3,4],"parent":"e1","depth":0}` {
		t.Errorf("node with fields: %s", b)
	}
	long := NodeOf(RawNode{Role: "textbox", Value: str(strings.Repeat("a", 81))}, "e3", "", false)
	if !long.Truncated || len(*long.Value) != 80 {
		t.Errorf("long value: %+v", long)
	}
}

func TestLinkCarriesURL(t *testing.T) {
	n := NodeOf(RawNode{Role: "link", Name: "Docs", URL: "https://example.com/docs"}, "e1", "", true)
	b, _ := json.Marshal(n)
	if !strings.Contains(string(b), `"url":"https://example.com/docs"`) {
		t.Errorf("link node: %s", b)
	}
	n = NodeOf(RawNode{Role: "link", Name: "Docs", URL: "https://example.com/docs"}, "e1", "", false)
	if n.URL != "" {
		t.Errorf("url without asking: %+v", n)
	}
}
