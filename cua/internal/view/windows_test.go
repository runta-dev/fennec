package view

import (
	"encoding/json"
	"strings"
	"testing"

	"fennec/cua/internal/protocol"
)

func win(key, title string, pid int, active bool) RawWindow {
	return RawWindow{Key: key, Title: title, PID: pid, Active: active, Role: "window"}
}

func hiddenWin(key, title string, pid int) RawWindow {
	w := win(key, title, pid, false)
	w.Hidden = "minimized"
	return w
}

func pickTitle(t *testing.T, ws []RawWindow, want string, pid int) (string, error) {
	t.Helper()
	w, err := PickWindow(ws, want, pid)
	return w.Key, err
}

func TestWindowPickTiers(t *testing.T) {
	ws := []RawWindow{
		win("1", "Mousepad Preferences", 7, true),
		win("2", "Untitled 1 - Mousepad", 7, false),
		win("3", "Settings", 9, false),
		win("4", "", 4, false),
	}
	for _, c := range []struct {
		want string
		pid  int
		key  string
	}{
		{"", 0, "1"},                      // the active window
		{"Settings", 0, "3"},              // exact
		{"  settings ", 0, "3"},           // trimmed, case-insensitive
		{"Mousepad", 0, "1"},              // contained; the active one
		{"Mousepad", 7, "1"},              // within the pid
		{"untitled 1 - mousepad", 0, "2"}, // tier 1 beats tier 2
		{"", 9, "3"},                      // a pid alone takes its front window
		{"", 7, "1"},                      //
	} {
		got, err := pickTitle(t, ws, c.want, c.pid)
		if err != nil || got != c.key {
			t.Errorf("pick(%q, %d) = %q, %v; want %q", c.want, c.pid, got, err, c.key)
		}
	}
	// Not within that pid.
	if _, err := pickTitle(t, ws, "Mousepad", 9); code(t, err) != "window_not_found" {
		t.Errorf("Mousepad in pid 9: %v", err)
	}
	// An untitled plain window cannot stand for the front one.
	if _, err := pickTitle(t, ws, "", 4); code(t, err) != "window_not_found" {
		t.Errorf("untitled window by pid: %v", err)
	}
	// A pid alone picks its window while another app has the focus.
	ws2 := []RawWindow{win("t", "Terminal", 4, true), hiddenWin("m1", "Untitled 1 - Mousepad", 7), win("m2", "Mousepad Preferences", 7, false)}
	if got, err := pickTitle(t, ws2, "", 7); err != nil || got != "m2" {
		t.Errorf("pid 7: %q %v", got, err)
	}
	// Different titles at the contains tier with none active are ambiguous.
	ws3 := []RawWindow{win("a", "Report draft", 1, false), win("b", "Report final", 1, false)}
	if _, err := pickTitle(t, ws3, "report", 0); code(t, err) != "target_ambiguous" || !strings.Contains(hint(err), "Report final") {
		t.Errorf("contained twins: %v", err)
	}
	// No focused window at all.
	if _, err := pickTitle(t, ws3, "", 0); code(t, err) != "window_not_found" {
		t.Errorf("no focus: %v", err)
	}
}

func TestWindowPickEqualTitles(t *testing.T) {
	// The active one wins, even hidden.
	active := hiddenWin("1", "Report", 1)
	active.Active = true
	if got, _ := pickTitle(t, []RawWindow{win("2", "Report", 2, false), active}, "Report", 0); got != "1" {
		t.Errorf("active twin: %q", got)
	}
	// None active: the front-most shown one.
	if got, _ := pickTitle(t, []RawWindow{win("f", "Report", 1, false), win("b", "Report", 2, false)}, "Report", 0); got != "f" {
		t.Errorf("front-most twin: %q", got)
	}
	// A lone hidden or minimized window is still reachable.
	if got, err := pickTitle(t, []RawWindow{win("n", "Untitled - Notes", 4, true), hiddenWin("d", "Doc", 5)}, "Doc", 0); err != nil || got != "d" {
		t.Errorf("lone minimized: %q %v", got, err)
	}
	// Focus and close of one of two same-titled windows take the active one.
	ws := []RawWindow{win("x", "Inbox", 3, false), win("y", "Inbox", 3, true)}
	if got, _ := pickTitle(t, ws, "Inbox", 3); got != "y" {
		t.Errorf("active of two: %q", got)
	}
}

func TestPickShownOverHiddenTwin(t *testing.T) {
	const title = "Messenger"
	hidden := win("h", title, 9, false)
	hidden.Hidden = "hidden"
	shown := win("s", title, 9, false)
	// The hidden twin is listed first (in front) and still loses.
	if got, err := pickTitle(t, []RawWindow{hidden, shown}, title, 0); err != nil || got != "s" {
		t.Errorf("shown twin: %q %v", got, err)
	}
	if got, err := pickTitle(t, []RawWindow{hidden, shown}, "", 9); err != nil || got != "s" {
		t.Errorf("by pid: %q %v", got, err)
	}
	// Two shown twins (equal frames both count): the front-most.
	other := win("s2", title, 9, false)
	if got, _ := pickTitle(t, []RawWindow{hidden, shown, other}, title, 0); got != "s" {
		t.Errorf("two shown: %q", got)
	}
	// The app's only window of that title can still be picked.
	if got, err := pickTitle(t, []RawWindow{hidden, win("o", "Other", 9, false)}, title, 0); err != nil || got != "h" {
		t.Errorf("lone hidden: %q %v", got, err)
	}
	// Twins across Spaces are refused.
	away := win("a", title, 9, false)
	away.Space = "other"
	if _, err := pickTitle(t, []RawWindow{shown, away}, title, 0); code(t, err) != "target_ambiguous" {
		t.Errorf("twins across Spaces: %v", err)
	}
}

func TestStaleTitleFallback(t *testing.T) {
	one := []RawWindow{win("t", "images - Thunar", 4, false), win("m", "notes.txt - Mousepad", 7, true)}
	if got, err := pickTitle(t, one, "inbox - Thunar", 0); err != nil || got != "t" {
		t.Errorf("one app window: %q %v", got, err)
	}
	active := []RawWindow{win("i", "images — Writer", 4, false), win("d", "docs — Writer", 5, true)}
	if got, err := pickTitle(t, active, "inbox — Writer", 0); err != nil || got != "d" {
		t.Errorf("active app window: %q %v", got, err)
	}
	none := []RawWindow{win("i", "images - Thunar", 4, false), win("d", "docs - Thunar", 5, false), win("m", "notes.txt - Mousepad", 7, true)}
	_, err := pickTitle(t, none, "inbox - Thunar", 0)
	if code(t, err) != "window_not_found" || hint(err) != `windows of its app: "images - Thunar", "docs - Thunar"` {
		t.Errorf("ambiguous app windows: %v / %q", err, hint(err))
	}
	_, err = pickTitle(t, []RawWindow{win("a", "a", 1, false)}, "report - Writer", 0)
	if code(t, err) != "window_not_found" || hint(err) != `windows: "a"` {
		t.Errorf("no app window: %v / %q", err, hint(err))
	}
}

func TestElidedTitlesMatchByNumber(t *testing.T) {
	const full = "Competitive Analysis: Pricing and Go-To-Market (October) - Example Docs - Google Chrome – Work"
	listed := []RawWindow{
		{Key: "cg:41", Title: "Competitive Analysis: Pric…ber) - Example Docs", PID: 5, Bounds: box(0, 0, 800, 600)},
		{Key: "cg:42", Title: "Inbox", PID: 5, Bounds: box(900, 0, 400, 400)},
	}
	ax := []RawWindow{{Key: "cg:41", Title: full, PID: 5}, {Key: "cg:42", Title: "Inbox", PID: 5}}
	ws := FullTitles(listed, ax)
	if ws[0].Title != full || ws[1].Title != "Inbox" || listed[0].Title == full {
		t.Fatalf("titles: %+v", ws)
	}
	if got, err := pickTitle(t, ws, full, 0); err != nil || got != "cg:41" {
		t.Errorf("full title: %q %v", got, err)
	}
	_, err := pickTitle(t, ws, "Quarterly - Sheets", 0)
	if code(t, err) != "window_not_found" || !strings.Contains(hint(err), "Go-To-Market") {
		t.Errorf("not-found lists full titles: %q", hint(err))
	}
	at, err := WindowAt(ws, 10, 10)
	if err != nil || at.Title != full {
		t.Errorf("point names the full title: %+v %v", at, err)
	}
}

func TestPointHitTopWindow(t *testing.T) {
	front := win("f", "Front", 1, true)
	front.Bounds = box(0, 0, 100, 100)
	top := win("t", "Palette", 2, false)
	top.Bounds = box(500, 500, 100, 100)
	under := win("u", "Canvas", 2, false)
	under.Bounds = box(400, 400, 400, 400)
	ws := []RawWindow{front, top, under}
	// Not the front window: the topmost window at the point.
	if w, err := WindowAt(ws, 550, 550); err != nil || w.Key != "t" {
		t.Errorf("topmost: %+v %v", w, err)
	}
	if w, err := WindowAt(ws, 450, 450); err != nil || w.Key != "u" {
		t.Errorf("under: %+v %v", w, err)
	}
	// Twins: the one whose bounds hold the point.
	a := win("a", "Doc", 3, false)
	a.Bounds = box(0, 0, 50, 50)
	b := win("b", "Doc", 3, false)
	b.Bounds = box(60, 0, 50, 50)
	if w, err := WindowAt([]RawWindow{a, b}, 70, 10); err != nil || w.Key != "b" {
		t.Errorf("twin by bounds: %+v %v", w, err)
	}
	c := b
	c.Key = "c"
	if _, err := WindowAt([]RawWindow{b, c}, 70, 10); code(t, err) != "target_ambiguous" {
		t.Errorf("indistinguishable twins: %v", err)
	}
	// No app window: a hint to name a window or pid.
	_, err := WindowAt(ws, 5000, 5000)
	if code(t, err) != "window_not_found" || !strings.Contains(hint(err), "name a window or pid") {
		t.Errorf("no window: %v", err)
	}
	// Minimized windows are not hit.
	m := hiddenWin("m", "Min", 4)
	m.Bounds = box(0, 0, 1000, 1000)
	if w, _ := WindowAt([]RawWindow{m, under}, 450, 450); w.Key != "u" {
		t.Errorf("minimized hit: %+v", w)
	}

	// Through observe: the element under the point names its process.
	el := RawNode{Handle: &handle{1}, Role: "button", Name: "Brush"}
	src := &fakeSource{windows: ws, at: fakeAt{node: &el, path: []RawNode{{Handle: &handle{2}, Role: "toolbar"}}}}
	o := &Observer{Source: src, Refs: NewRegistry(0)}
	res, err := o.Observe(ctx(), obsReq{target: &protocol.Target{Point: &protocol.Point{X: 550, Y: 550}}}.req())
	if err != nil || res.At == nil || res.At.Window.Title != "Palette" || res.At.Window.PID != 2 || res.At.Element.Name != "Brush" || len(res.At.Path) != 1 {
		t.Fatalf("observe point: %+v %v", res, err)
	}
	if !strings.HasPrefix(res.At.Element.Ref, "e") || res.At.Covered != nil {
		t.Errorf("hit refs: %+v", res.At)
	}
	if _, err := o.Observe(ctx(), obsReq{target: &protocol.Target{Point: &protocol.Point{X: 1, Y: 1, Space: "image"}}}.req()); code(t, err) != "invalid_argument" {
		t.Errorf("image point: %v", err)
	}
}

func TestPointHitCover(t *testing.T) {
	target := win("t", "Editor", 2, false)
	target.Bounds = box(0, 0, 500, 500)
	cover := win("c", "Notification", 8, false)
	cover.Bounds = box(0, 0, 200, 100)
	el := RawNode{Handle: &handle{1}, Role: "text", Name: "line"}
	src := &fakeSource{windows: []RawWindow{cover, target}, at: fakeAt{node: &el, window: &target, covered: &cover}}
	o := &Observer{Source: src, Refs: NewRegistry(0)}
	res, err := o.Observe(ctx(), obsReq{target: &protocol.Target{Point: &protocol.Point{X: 10, Y: 10}}}.req())
	if err != nil || res.At.Covered == nil || res.At.Covered.Title != "Notification" || res.At.Covered.PID != 8 || res.At.Window.Title != "Editor" {
		t.Fatalf("covered: %+v %v", res.At, err)
	}
	b, _ := json.Marshal(res.At)
	if !strings.Contains(string(b), `"covered":{"ref":"w`) {
		t.Errorf("covered json: %s", b)
	}
}

func TestWindowListFields(t *testing.T) {
	doc := RawWindow{Key: "cg:1", App: "TextEdit", PID: 10, Title: "Doc", Role: "window", Active: true, Bounds: box(0, 0, 640, 480)}
	sheet := RawWindow{Key: "ax:2", App: "TextEdit", PID: 10, Title: "", Role: "sheet", Active: true, OwnerKey: "cg:1"}
	bare := RawWindow{Key: "cg:3", Title: "Unknown app", Role: "window"}
	src := &fakeSource{windows: []RawWindow{doc, sheet, bare}}
	o := &Observer{Source: src, Refs: NewRegistry(0), Input: protocol.InputForeground}
	res, err := o.Observe(ctx(), obsReq{scope: "desktop"}.req())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Windows) != 3 || res.View == "" || res.Window == nil || res.Window.Title != "Doc" {
		t.Fatalf("windows: %+v", res)
	}
	got, _ := json.Marshal(res.Windows)
	want := `[{"ref":"w1","app":"TextEdit","title":"Doc","role":"window","pid":10,"active":true,"bounds":[0,0,640,480]},` +
		`{"ref":"w2","app":"TextEdit","title":"","role":"sheet","pid":10,"active":true,"owner":"w1"},` +
		`{"ref":"w3","title":"Unknown app","role":"window","active":false}]`
	if string(got) != want {
		t.Errorf("window list:\n%s\nwant:\n%s", got, want)
	}
	if len(src.treeCalls) != 0 {
		t.Errorf("desktop scope walked a tree")
	}
}

func TestStaleWindowListedNotActedOn(t *testing.T) {
	stale := RawWindow{Key: "cg:5", App: "Hung", PID: 50, Title: "Busy doc", Role: "window", Stale: true}
	live := RawWindow{Key: "cg:6", App: "Fine", PID: 60, Title: "Live", Role: "window", Active: true}
	src := &fakeSource{
		windows: []RawWindow{stale, live},
		trees:   map[string]Tree{"cg:5": {Nodes: build(spec{role: "button", name: "Save"})}, "cg:6": {}},
		reads:   map[Handle]RawNode{},
	}
	o := &Observer{Source: src, Refs: NewRegistry(0)}
	res, err := o.Observe(ctx(), obsReq{scope: "desktop"}.req())
	if err != nil || len(res.Windows) != 2 || !res.Windows[0].Stale {
		t.Fatalf("stale window not listed: %+v %v", res, err)
	}
	for _, r := range []obsReq{
		{window: "Busy doc"},
		{window: res.Windows[0].Ref},
		{window: "Busy doc", find: `button "Save"`},
		{target: &protocol.Target{Select: `button "Save"`, Window: "Busy doc"}},
	} {
		_, err := o.Observe(ctx(), r.req())
		if code(t, err) != "app_not_responding" || !strings.Contains(err.Error(), "busy or hung") {
			t.Errorf("%+v: %v", r, err)
		}
	}
	if len(src.treeCalls) != 0 || src.readCalls != 0 {
		t.Errorf("stale window touched: %d trees, %d reads", len(src.treeCalls), src.readCalls)
	}
	// Close and other actions use the same check.
	if err := Actionable(stale, protocol.InputForeground); code(t, err) != "app_not_responding" {
		t.Errorf("actionable: %v", err)
	}
}

func TestOtherSpaceWindowsListedInBackground(t *testing.T) {
	here := RawWindow{Key: "cg:1", Title: "Here", Role: "window", Active: true}
	away := RawWindow{Key: "cg:2", Title: "Away", Role: "window", Space: "other", Active: true}
	for input, n := range map[string]int{protocol.InputForeground: 1, protocol.InputBackground: 2, protocol.InputAuto: 2} {
		o := &Observer{Source: &fakeSource{windows: []RawWindow{here, away}}, Refs: NewRegistry(0), Input: input}
		res, err := o.Observe(ctx(), obsReq{scope: "desktop"}.req())
		if err != nil || len(res.Windows) != n {
			t.Errorf("%s: %+v %v", input, res, err)
			continue
		}
		if n == 2 && (res.Windows[1].Space != "other" || res.Windows[1].Active) {
			t.Errorf("%s: other-Space window %+v", input, res.Windows[1])
		}
	}
}

func TestOutlineOfOtherSpaceWindowRefused(t *testing.T) {
	here := RawWindow{Key: "cg:1", Title: "Here", Role: "window", Active: true}
	away := RawWindow{Key: "cg:2", Title: "Away", Role: "window", Space: "other"}
	src := &fakeSource{windows: []RawWindow{here, away}, trees: map[string]Tree{"cg:1": {}, "cg:2": {}}}
	o := &Observer{Source: src, Refs: NewRegistry(0), Input: protocol.InputBackground}
	desk, err := o.Observe(ctx(), obsReq{scope: "desktop"}.req())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []obsReq{{window: "Away"}, {window: desk.Windows[1].Ref}, {window: "Away", find: `"x"`}} {
		_, err := o.Observe(ctx(), r.req())
		if code(t, err) != "off_space" || !strings.Contains(err.Error(), "another Space") {
			t.Errorf("%+v: %v", r, err)
		}
	}
	// An unknown title is the ordinary not-found, without Spaces.
	_, err = o.Observe(ctx(), obsReq{window: "Nowhere - App"}.req())
	if code(t, err) != "window_not_found" || strings.Contains(err.Error()+hint(err), "Space") {
		t.Errorf("unknown title: %v / %s", err, hint(err))
	}
	if len(src.treeCalls) != 0 {
		t.Errorf("walked a window on another Space")
	}
	// Auto input may still reach it (the caller brings it forward).
	o.Input = protocol.InputAuto
	if _, err := o.Observe(ctx(), obsReq{window: "Away"}.req()); err != nil {
		t.Errorf("auto: %v", err)
	}
}
