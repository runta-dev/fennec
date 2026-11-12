package view

import (
	"reflect"
	"strings"
	"testing"

	"fennec/cua/internal/protocol"
)

// readSetup is a window with one long text area and its read.
func readSetup(value string) (*Observer, *fakeSource) {
	w := RawWindow{Key: "cg:1", Title: "Terminal", App: "Terminal", Role: "window", Active: true, PID: 3}
	nodes := build(
		spec{depth: 0, role: "window", name: "Terminal"},
		spec{depth: 1, role: "textbox", name: "shell", value: str(value)},
	)
	nodes[1].Page = "https://example.com/app?q=1"
	nodes[1].DOMID = "term"
	src := &fakeSource{
		windows: []RawWindow{w},
		trees:   map[string]Tree{w.Key: {Nodes: nodes}},
		reads:   map[Handle]RawNode{nodes[1].Handle: nodes[1]},
	}
	return &Observer{Source: src, Refs: NewRegistry(0)}, src
}

func readRef(t *testing.T, o *Observer) string {
	t.Helper()
	res, err := o.Observe(ctx(), obsReq{find: `textbox "shell"`, nodes: true}.req())
	if err != nil || len(res.Nodes) != 1 {
		t.Fatalf("find: %+v %v", res, err)
	}
	return res.Nodes[0].Ref
}

func TestReadPreviewAndFullText(t *testing.T) {
	long := strings.Repeat("a", 50) + strings.Repeat("b", 50) + "end"
	o, _ := readSetup(long)
	ref := readRef(t, o)
	elem := func(r obsReq) (*protocol.ObserveResult, error) {
		r.target = &protocol.Target{Ref: ref}
		return o.Observe(ctx(), r.req())
	}
	// No full text: the whole name and an 80-character preview, no flag.
	res, err := elem(obsReq{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Element.Name != "shell" || *res.Element.Value != long[:80] || res.Element.Truncated {
		t.Errorf("preview: %+v", res.Element)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "text: true") {
		t.Errorf("preview note: %v", res.Notes)
	}
	// Full text: all of it, not truncated.
	res, err = elem(obsReq{text: true})
	if err != nil || *res.Element.Value != long || res.Element.Truncated || len(res.Notes) != 0 {
		t.Errorf("full: %+v %v %v", res.Element, res.Notes, err)
	}
	// Tail without text is an error.
	if _, err := elem(obsReq{tail: true}); code(t, err) != "invalid_argument" || !strings.Contains(err.Error(), "tail needs text") {
		t.Errorf("tail alone: %v", err)
	}
	// A selector target reads too.
	res, err = o.Observe(ctx(), obsReq{target: &protocol.Target{Select: `textbox "shell"`}}.req())
	if err != nil || res.Element.Ref == "" || res.Element.Name != "shell" {
		t.Errorf("selector read: %+v %v", res, err)
	}
	// A window ref is not an element; an evicted ref is stale.
	if _, err := o.Observe(ctx(), obsReq{target: &protocol.Target{Ref: res.Windows[0].Ref}}.req()); code(t, err) != "invalid_argument" {
		t.Errorf("window ref read: %v", err)
	}
	if _, err := o.Observe(ctx(), obsReq{target: &protocol.Target{Ref: "e999"}}.req()); code(t, err) != "stale_ref" {
		t.Errorf("unknown ref read: %v", err)
	}
	// A preview of a short value has no note.
	n, notes := ReadNode(RawNode{Role: "textbox", Value: str("short")}, "e1", false, false, false, false)
	if *n.Value != "short" || len(notes) != 0 || n.Truncated {
		t.Errorf("short preview: %+v %v", n, notes)
	}
}

func TestReadPreviewAndTail(t *testing.T) {
	// Longer than the full-text limit, multi-byte at both ends.
	head := "€" + strings.Repeat("h", MaxText)
	value := head + strings.Repeat("t", 10) + "🎉"
	o, _ := readSetup(value)
	ref := readRef(t, o)
	read := func(text, tail bool) *protocol.Node {
		res, err := o.Observe(ctx(), obsReq{text: text, tail: tail, target: &protocol.Target{Ref: ref}}.req())
		if err != nil {
			t.Fatal(err)
		}
		return res.Element
	}
	preview := read(false, false)
	if []rune(*preview.Value)[0] != '€' || len([]rune(*preview.Value)) != PreviewChars || preview.Truncated {
		t.Errorf("preview: %d chars, truncated %v", len([]rune(*preview.Value)), preview.Truncated)
	}
	full := read(true, false)
	if r := []rune(*full.Value); len(r) != MaxText || r[0] != '€' || !full.Truncated {
		t.Errorf("head: %d chars, truncated %v", len(r), full.Truncated)
	}
	tail := read(true, true)
	if r := []rune(*tail.Value); len(r) != MaxText || !strings.HasSuffix(*tail.Value, "tttttttttt🎉") || !tail.Truncated {
		t.Errorf("tail: %d chars, truncated %v", len(r), tail.Truncated)
	}
	// A short tail read is whole and not truncated.
	o2, _ := readSetup("$ ls\nfile\n$ ")
	ref2 := readRef(t, o2)
	res, err := o2.Observe(ctx(), obsReq{text: true, tail: true, target: &protocol.Target{Ref: ref2}}.req())
	if err != nil || *res.Element.Value != "$ ls\nfile\n$ " || res.Element.Truncated {
		t.Errorf("short tail: %+v %v", res.Element, err)
	}
	// The backend saying it cut the text flags a full read as truncated.
	n, _ := ReadNode(RawNode{Value: str("abc")}, "e1", true, true, false, true)
	if !n.Truncated {
		t.Error("backend cut not flagged")
	}
}

func TestReadWebUrlDomId(t *testing.T) {
	o, _ := readSetup("x")
	ref := readRef(t, o)
	plain, err := o.Observe(ctx(), obsReq{target: &protocol.Target{Ref: ref}}.req())
	if err != nil || plain.Element.URL != "" || plain.Element.DOMID != "" || len(plain.Notes) != 0 {
		t.Errorf("plain read: %+v %v %v", plain.Element, plain.Notes, err)
	}
	web, err := o.Observe(ctx(), obsReq{web: true, target: &protocol.Target{Ref: ref}}.req())
	if err != nil || web.Element.URL != "https://example.com/app?q=1" || web.Element.DOMID != "term" {
		t.Errorf("web read: %+v %v", web.Element, err)
	}
	if !reflect.DeepEqual(web.Notes, []string{"page: https://example.com/app?q=1"}) {
		t.Errorf("web notes: %v", web.Notes)
	}
	// A link keeps its own target and gives the page as a note.
	n, notes := ReadNode(RawNode{Role: "link", URL: "https://example.com/next", Page: "https://example.com/", DOMID: "nx"}, "e1", false, false, true, false)
	if n.URL != "https://example.com/next" || n.DOMID != "nx" || len(notes) != 1 {
		t.Errorf("web link read: %+v %v", n, notes)
	}
}

func TestObserveDefaultsToFrontWindow(t *testing.T) {
	back := RawWindow{Key: "cg:2", Title: "Back", Role: "window"}
	front := RawWindow{Key: "cg:1", Title: "Front", Role: "window", Active: true, App: "A"}
	sheet := RawWindow{Key: "ax:3", Title: "Save", Role: "sheet", OwnerKey: "cg:1"}
	src := &fakeSource{
		windows: []RawWindow{front, sheet, back},
		trees: map[string]Tree{
			"cg:1": {Nodes: build(spec{depth: 0, role: "window", name: "Front"}, spec{depth: 1, role: "button", name: "Go"})},
			"ax:3": {Nodes: build(spec{depth: 0, role: "textbox", name: "Name"})},
		},
	}
	o := &Observer{Source: src, Refs: NewRegistry(0)}
	res, err := o.Observe(ctx(), protocol.ObserveRequest{})
	if err != nil || res.Window.Title != "Front" || !strings.HasPrefix(res.Outline, res.Window.Ref+` window "Front" (A) [active]`) {
		t.Fatalf("default observe: %+v %v", res, err)
	}
	if res.View != "v1" || len(res.Nodes) != 0 {
		t.Errorf("view %q nodes %d", res.View, len(res.Nodes))
	}
	res, err = o.Observe(ctx(), obsReq{window: "Save", nodes: true}.req())
	if err != nil || res.Window.Owner != res.Windows[0].Ref || len(res.Nodes) != 1 || res.Nodes[0].Name != "Name" {
		t.Errorf("sheet observe: %+v %v", res, err)
	}
	_, err = o.Observe(ctx(), obsReq{find: `button "Stop"`}.req())
	var pe *protocol.Error
	if code(t, err) != "target_not_found" {
		t.Errorf("find miss: %v", err)
	} else if pe = err.(*protocol.Error); pe.Observe == nil || pe.Observe.Window == nil {
		t.Errorf("find miss carries no view: %+v", pe)
	}
	if _, err := o.Observe(ctx(), obsReq{scope: "bogus"}.req()); code(t, err) != "invalid_argument" {
		t.Errorf("bad scope: %v", err)
	}
}
