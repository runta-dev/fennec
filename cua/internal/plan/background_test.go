package plan

import (
	"strings"
	"testing"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// behind is Safari in front and Mail's "Inbox" (cg:2, pid 200) behind it.
func behind(t *testing.T, input string) *rig {
	r := newRig(t, policy.Config{Input: input})
	r.d.Win("cg:9", "Safari", "Start Page", 900, [4]int{0, 0, 800, 600}, true)
	r.d.Win("cg:2", "Mail", "Inbox", 200, [4]int{100, 100, 900, 700}, false)
	return r
}

func keyCalls(d *desk) []fakedesk.Call {
	var out []fakedesk.Call
	for _, c := range d.Calls {
		if c.Tool == "press_key" || c.Tool == "hotkey" {
			out = append(out, c)
		}
	}
	return out
}

func TestBackgroundTargetsPlanWindow(t *testing.T) {
	r := behind(t, "background")
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+n"}, protocol.Step{Key: "tab"})
	mustPass(t, res)
	keys := keyCalls(r.d)
	if len(keys) != 2 {
		t.Fatalf("%s", r.d)
	}
	for _, k := range keys {
		if k.Arg("pid") != "200" || k.Arg("window_id") != "2" || k.Arg("delivery_mode") != `background` {
			t.Fatalf("background input does not name the plan window: %s", r.d)
		}
	}
	if len(r.d.Raised) != 0 || launches(r.d) != 0 {
		t.Fatalf("something was fronted or launched: %s", r.d)
	}
	// A plan with no app or wait step has no target window.
	r = behind(t, "background")
	res = r.run(protocol.Step{Key: "cmd+s"})
	mustStop(t, res, 0, protocol.ErrWindowNotFound)
	if len(r.d.Calls) != 0 || !strings.Contains(res.Error.Message, "no target window") {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
}

func TestBackgroundWindowIdentity(t *testing.T) {
	// Another window of the app with the same title: no guess.
	r := behind(t, "background")
	r.d.Win("cg:3", "Mail", "Inbox", 200, [4]int{120, 120, 900, 700}, false)
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+n"})
	mustStop(t, res, 1, protocol.ErrTargetAmbiguous)
	if len(keyCalls(r.d)) != 0 {
		t.Fatal(r.d)
	}
	// A window that reuses the plan window's id under another process is
	// not the plan window.
	r = behind(t, "background")
	r.d.onDriver = func(tool string, _ map[string]any) {
		if tool == "press_key" || tool == "hotkey" {
			r.d.Window("cg:2").PID = 999
		}
	}
	res = r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+n"}, protocol.Step{Key: "tab"})
	mustStop(t, res, 2, protocol.ErrWindowNotFound)
	if len(keyCalls(r.d)) != 1 {
		t.Fatal(r.d)
	}
	// Once the plan window is gone, a same-titled window of the app never
	// replaces it.
	r = behind(t, "background")
	r.d.onDriver = func(tool string, _ map[string]any) {
		if tool == "press_key" || tool == "hotkey" {
			r.d.Wins = []view.RawWindow{r.d.Wins[0], {Key: "cg:4", Handle: "cg:4", App: "Mail", PID: 200, Title: "Inbox", Role: "window"}}
		}
	}
	res = r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+w"}, protocol.Step{Key: "tab"})
	mustStop(t, res, 2, protocol.ErrWindowNotFound)
	if !strings.Contains(res.Error.Message, "never takes another window") || len(keyCalls(r.d)) != 1 {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
}

func TestBackgroundDialogOwnership(t *testing.T) {
	open := func(r *rig, ws ...view.RawWindow) {
		done := false
		r.d.onDriver = func(tool string, _ map[string]any) {
			if !done && (tool == "press_key" || tool == "hotkey") {
				done = true
				r.d.Wins = append(r.d.Wins, ws...)
			}
		}
	}
	// A dialog the plan window owns becomes the target.
	r := behind(t, "background")
	open(r, view.RawWindow{Key: "cg:5", Handle: "cg:5", App: "Mail", PID: 200, Title: "Save", Role: "dialog", OwnerKey: "cg:2"})
	mustPass(t, r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+s"}, protocol.Step{Key: "tab"}))
	if k := keyCalls(r.d); k[1].Arg("window_id") != "5" {
		t.Fatalf("%s", r.d)
	}
	// A dialog owned by another window of the app never does.
	r = behind(t, "background")
	r.d.Win("cg:3", "Mail", "Drafts", 200, [4]int{0, 0, 10, 10}, false)
	open(r, view.RawWindow{Key: "cg:5", Handle: "cg:5", App: "Mail", PID: 200, Title: "Save", Role: "dialog", OwnerKey: "cg:3"})
	mustPass(t, r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Window: "Inbox", Key: "cmd+s"}, protocol.Step{Key: "tab"}))
	if k := keyCalls(r.d); k[1].Arg("window_id") != "2" {
		t.Fatalf("%s", r.d)
	}
	// Two new dialogs no owner names: refused, not guessed.
	r = behind(t, "background")
	open(r, view.RawWindow{Key: "cg:5", Handle: "cg:5", App: "Mail", PID: 200, Title: "A", Role: "dialog"},
		view.RawWindow{Key: "cg:6", Handle: "cg:6", App: "Mail", PID: 200, Title: "B", Role: "dialog"})
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+s"}, protocol.Step{Key: "tab"})
	mustStop(t, res, 2, protocol.ErrTargetAmbiguous)
	if len(keyCalls(r.d)) != 1 {
		t.Fatal(r.d)
	}
}

func TestBackgroundSheetInput(t *testing.T) {
	r := behind(t, "background")
	done := false
	r.d.onDriver = func(tool string, _ map[string]any) {
		if !done && (tool == "press_key" || tool == "hotkey") {
			done = true
			r.d.Wins = append(r.d.Wins, view.RawWindow{Key: "ax:sheet1", Handle: "ax:sheet1", App: "Mail", PID: 200, Title: "", Role: "sheet", OwnerKey: "cg:2"})
		}
	}
	mustPass(t, r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+s"}, protocol.Step{Key: "tab"}))
	if k := keyCalls(r.d); len(k) != 2 || k[1].Arg("window_id") != "2" || k[1].Arg("pid") != "200" {
		t.Fatalf("input for a sheet goes to the window it hangs on: %s", r.d)
	}
}

func TestBackgroundFrontStealFails(t *testing.T) {
	r := behind(t, "background")
	r.d.onDriver = func(tool string, _ map[string]any) {
		if tool == "press_key" || tool == "hotkey" {
			for i := range r.d.Wins {
				r.d.Wins[i].Active = r.d.Wins[i].Key == "cg:2"
			}
		}
	}
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+n"}, protocol.Step{Key: "tab"})
	mustStop(t, res, 1, protocol.ErrFocusMismatch)
	if !strings.Contains(res.Error.Message, "came to the front by itself") || len(keyCalls(r.d)) != 1 {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
}

func TestBackgroundNoScreenReads(t *testing.T) {
	r := behind(t, "background")
	m := r.models()
	scr := newScreen(100, 100)
	r.e.Screen, r.e.VisionModel = scr, true
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Click: sel(`button "Archive"`)})
	mustStop(t, res, 1, protocol.ErrTargetNotFound)
	if !strings.Contains(res.Error.Message, "does not read the screen") || scr.shots != 0 || m.grounds != 0 || len(r.d.Calls) != 0 {
		t.Fatalf("%+v shots %d grounds %d %s", res.Error, scr.shots, m.grounds, r.d)
	}
	// An expectation the tree cannot settle fails the same way, without a
	// screenshot, a visual question or a focus change.
	r = behind(t, "background")
	m = r.models()
	scr = newScreen(100, 100)
	r.e.Screen = scr
	res = r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Expect: &protocol.Expect{Looks: "is the message sent?"}})
	mustStop(t, res, 1, protocol.ErrExpectFailed)
	if scr.shots != 0 || m.asks != 0 || len(r.d.Raised) != 0 {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
}

func TestAutoMatchesBackground(t *testing.T) {
	plan := []protocol.Step{{Launch: "Mail"}, {Key: "cmd+n"}, {Key: "tab"}}
	bg := behind(t, "background")
	mustPass(t, bg.run(plan...))
	auto := behind(t, "auto")
	mustPass(t, auto.run(plan...))
	if bg.d.String() != auto.d.String() {
		t.Fatalf("auto differs from background:\n%s\n---\n%s", bg.d, auto.d)
	}
}

// field puts a focused text field in Mail whose value the test controls.
func field(r *rig, value string, states ...string) *view.RawNode {
	n := &view.RawNode{Handle: "fld", Role: "textbox", Name: "To", Value: &value, States: append([]string{"focused"}, states...), Bounds: fakedesk.Box(200, 200, 100, 20)}
	r.d.Focus[200] = []*view.RawNode{n}
	return n
}

func typeCalls(d *desk) []fakedesk.Call {
	var out []fakedesk.Call
	for _, c := range d.Calls {
		if c.Tool == "type_text" {
			out = append(out, c)
		}
	}
	return out
}

func TestAutoForegroundFallback(t *testing.T) {
	r := behind(t, "auto")
	field(r, "")
	r.d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("the app did not report the text"), fakedesk.OK}
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Type: str("hello")}, protocol.Step{Key: "tab"})
	mustPass(t, res)
	ty := typeCalls(r.d)
	if len(ty) != 2 || ty[0].Arg("delivery_mode") != "background" || ty[1].Arg("delivery_mode") != "foreground" {
		t.Fatalf("%s", r.d)
	}
	if res.Steps[1].Input != protocol.InputForeground || strings.Join(r.d.Raised, ",") != "cg:2" {
		t.Fatalf("%s %v", jsonOf(res.Steps[1]), r.d.Raised)
	}
	// Background mode stops with "did not show" instead.
	r = behind(t, "background")
	field(r, "")
	r.d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("the app did not report the text")}
	res = r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Type: str("hello")})
	mustStop(t, res, 1, protocol.ErrNotShown)
	if len(typeCalls(r.d)) != 1 {
		t.Fatal(r.d)
	}
}

func TestAutoNoRetypeWhenUnprovable(t *testing.T) {
	cases := map[string]func(r *rig){
		"landed in part": func(r *rig) {
			n := field(r, "")
			r.d.onDriver = func(tool string, _ map[string]any) {
				if tool == "type_text" {
					v := "hel"
					n.Value = &v
				}
			}
		},
		"secure field": func(r *rig) { field(r, "", "protected") },
		"unreadable after": func(r *rig) {
			field(r, "")
			r.d.onDriver = func(tool string, _ map[string]any) {
				if tool == "type_text" {
					r.d.FocusErr = errNoFocus
				}
			}
		},
		"long field": func(r *rig) { field(r, strings.Repeat("x", view.MaxText)) },
	}
	for name, setup := range cases {
		r := behind(t, "auto")
		setup(r)
		r.d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("not reported")}
		res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Type: str("hello")})
		if res.OK || res.StoppedAt == nil || *res.StoppedAt != 1 {
			t.Fatalf("%s: %s", name, jsonOf(res))
		}
		if len(typeCalls(r.d)) != 1 || len(r.d.Raised) != 0 {
			t.Fatalf("%s: retyped or fronted: %s", name, r.d)
		}
	}
}

// menus gives Mail's window a menu bar: File > New.
func menus(r *rig, win string) {
	r.d.Add(win, "bar", "menubar", "", nil, win)
	r.d.Add(win, "file", "menubaritem", "File", nil, "bar")
	r.d.Add(win, "filemenu", "menu", "File", nil, "file")
	r.d.Add(win, "new", "menuitem", "New", nil, "filemenu")
}

func TestMenuStepBackgroundRefused(t *testing.T) {
	r := behind(t, "background")
	menus(r, "cg:2")
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Menu: "File > New"})
	mustStop(t, res, 1, protocol.ErrUnavailable)
	if len(r.d.Performs) != 0 || !strings.Contains(res.Error.Message, "menu bar belongs to the front app") {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
	// Auto fronts the plan window first and runs it in the foreground.
	r = behind(t, "auto")
	menus(r, "cg:2")
	res = r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Menu: "File > New"})
	mustPass(t, res)
	if strings.Join(r.d.Raised, ",") != "cg:2" || presses(r.d) != "file:AXPress,new:AXPress" {
		t.Fatalf("%v %v", r.d.Raised, r.d.Performs)
	}
}

func TestAutoKeepsForeground(t *testing.T) {
	r := behind(t, "auto")
	menus(r, "cg:2")
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Menu: "File > New"}, protocol.Step{Key: "cmd+v"}, protocol.Step{Key: "return"})
	mustPass(t, res)
	keys := keyCalls(r.d)
	if len(keys) != 2 {
		t.Fatal(r.d)
	}
	for _, k := range keys {
		if k.Arg("delivery_mode") != "foreground" {
			t.Fatalf("later input left the foreground: %s", r.d)
		}
	}
}

func TestHungFrontAppEscalates(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.onPerform = func(id, _ string) { r.d.Window("cg:1").Stale = true }
	res := r.run(protocol.Step{Press: sel(`button "Save"`)}, protocol.Step{Key: "cmd+s"})
	mustStop(t, res, 1, protocol.ErrNotResponding)
	if !strings.Contains(res.Error.Message, "after step 0") || !strings.Contains(res.Error.Hint, "resume from step 1") || len(r.d.Calls) != 0 {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
	// Another app comes to the front while it hangs: it gets nothing.
	r = newRig(t, policy.Config{}).notes()
	r.d.Win("cg:8", "Other", "Other", 800, [4]int{0, 0, 10, 10}, false)
	r.d.onPerform = func(id, _ string) { r.d.Window("cg:1").Stale = true }
	sleeps := 0
	r.clk.onSleep = func() {
		if sleeps++; sleeps == 2 {
			r.d.Window("cg:1").Active = false
			r.d.Window("cg:8").Active = true
		}
	}
	res = r.run(protocol.Step{Press: sel(`button "Save"`)}, protocol.Step{Key: "cmd+s"})
	if res.OK || len(keyCalls(r.d)) != 0 {
		t.Fatalf("%s %s", jsonOf(res), r.d)
	}
}

func TestHungFrontAppRecovers(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.onPerform = func(id, _ string) { r.d.Window("cg:1").Stale = true }
	sleeps := 0
	r.clk.onSleep = func() {
		if sleeps++; sleeps == 3 {
			r.d.Window("cg:1").Stale = false
		}
	}
	mustPass(t, r.run(protocol.Step{Press: sel(`button "Save"`)}, protocol.Step{Key: "cmd+s"}))
	// The stale window closes and the one that comes to the front answers.
	r = newRig(t, policy.Config{}).notes()
	r.d.Win("cg:8", "TextEdit", "Other", 100, [4]int{0, 0, 10, 10}, false)
	r.d.onPerform = func(id, _ string) { r.d.Window("cg:1").Stale = true }
	sleeps = 0
	r.clk.onSleep = func() {
		if sleeps++; sleeps == 2 {
			r.d.Wins = []view.RawWindow{*r.d.Window("cg:8")}
			r.d.Wins[0].Active = true
		}
	}
	mustPass(t, r.run(protocol.Step{Press: sel(`button "Save"`)}, protocol.Step{Key: "cmd+s"}))
}

func TestUnansweredWindowGetsNoInput(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Window("cg:1").Stale = true
	m := r.models()
	scr := newScreen(10, 10)
	r.e.Screen = scr
	for _, s := range []protocol.Step{{Press: sel(`button "Save"`)}, {Close: "Notes"}, {Type: str("x")}, {Key: "cmd+s"}} {
		res := r.act(protocol.ActRequest{Window: "Notes", Steps: []protocol.Step{s}})
		mustStop(t, res, 0, protocol.ErrNotResponding)
		if res.Steps[0].OK || res.Steps[0].Status == protocol.StatusConfirmed {
			t.Fatalf("recorded as a success: %s", jsonOf(res.Steps))
		}
	}
	if len(r.d.Calls) != 0 || len(r.d.Performs) != 0 || len(r.d.Closed) != 0 || scr.shots != 0 || m.calls() != 0 {
		t.Fatalf("an unanswering window got input: %s", r.d)
	}
	// With no window in front, the error names the front app that does
	// not answer.
	r = newRig(t, policy.Config{})
	r.d.Win("cg:3", "Hung", "", 300, [4]int{0, 0, 10, 10}, true).Stale = true
	res := r.run(protocol.Step{Key: "cmd+s"})
	mustStop(t, res, 0, protocol.ErrNotResponding)
	if !strings.Contains(res.Error.Message, "pid 300") {
		t.Fatalf("%+v", res.Error)
	}
}

func TestHiddenModalBlocks(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Win("cg:6", "TextEdit", "Do you want to save?", 100, [4]int{0, 0, 0, 0}, false).Role = "dialog"
	r.d.Window("cg:6").Hidden = "minimized"
	res := r.act(protocol.ActRequest{Window: "Notes", Steps: []protocol.Step{{Type: str("hello"), AnyFocus: true}}})
	mustStop(t, res, 0, protocol.ErrFocusMismatch)
	if !strings.Contains(res.Error.Message, `"Do you want to save?"`) || !strings.Contains(res.Error.Hint, "hidden modal") || len(r.d.Calls) != 0 {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
	// A close step aimed at the dialog is allowed.
	res = r.act(protocol.ActRequest{Window: "Notes", Steps: []protocol.Step{{Close: "Do you want to save?"}}})
	mustPass(t, res)
	// Steps in another app proceed.
	r.d.Win("cg:7", "Notes", "Shopping", 700, [4]int{0, 0, 100, 100}, false)
	res = r.act(protocol.ActRequest{Window: "Shopping", Steps: []protocol.Step{{Key: "cmd+n"}}})
	mustPass(t, res)
}

var errNoFocus = errorString("the focus cannot be read")

type errorString string

func (e errorString) Error() string { return string(e) }
