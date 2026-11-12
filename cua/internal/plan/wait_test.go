package plan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

func waitFront(title string) protocol.Step {
	return protocol.Step{Wait: &protocol.WaitRequest{Expect: protocol.Expect{Front: title, TimeoutMs: 500}}}
}

// frontless has no window in front.
func frontless(t *testing.T, titles ...string) *rig {
	r := newRig(t, policy.Config{})
	for i, title := range titles {
		key := "cg:" + string(rune('5'+i))
		r.d.Win(key, "TextEdit", title, 100, [4]int{0, 0, 300, 200}, false)
	}
	return r
}

func TestWaitTitleExactOverContains(t *testing.T) {
	r := frontless(t, "Save As", "Save")
	mustPass(t, r.run(waitFront("Save")))
	if strings.Join(r.d.Raised, ",") != "cg:6" {
		t.Fatal(r.d.Raised)
	}
	r = frontless(t, "Save As", "Save a Copy")
	res := r.run(waitFront("Save"), protocol.Step{Key: "return"})
	mustStop(t, res, 0, protocol.ErrTargetAmbiguous)
	if !strings.Contains(res.Error.Message, "2 windows match") || len(r.d.Calls) != 0 || len(r.d.Raised) != 0 {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
}

func TestWaitAmbiguousTitle(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.onPerform = func(id, _ string) {
		r.d.Win("cg:5", "TextEdit", "Report 1", 100, [4]int{0, 0, 10, 10}, false)
		r.d.Win("cg:6", "TextEdit", "Report 2", 100, [4]int{0, 0, 10, 10}, false)
	}
	res := r.run(protocol.Step{Press: sel(`button "Save"`), Expect: &protocol.Expect{WindowOpens: "Report"}})
	mustStop(t, res, 0, protocol.ErrTargetAmbiguous)
	if !strings.Contains(res.Error.Message, "2 windows match") {
		t.Fatal(res.Error)
	}
	// Exactly one title equal to the text is taken over the others.
	r = newRig(t, policy.Config{}).notes()
	r.d.onPerform = func(id, _ string) {
		r.d.Win("cg:5", "TextEdit", "Report 1", 100, [4]int{0, 0, 10, 10}, false)
		r.d.Win("cg:6", "TextEdit", "Report", 100, [4]int{0, 0, 10, 10}, false)
	}
	mustPass(t, r.run(protocol.Step{Press: sel(`button "Save"`), Expect: &protocol.Expect{WindowOpens: "Report"}}))
}

func TestWaitUniqueWindow(t *testing.T) {
	r := frontless(t, "Export")
	r.d.Add("cg:5", "ok", "button", "OK", nil, "cg:5")
	mustPass(t, r.run(waitFront("Export"), protocol.Step{Press: sel(`button "OK"`)}))
	if strings.Join(r.d.Raised, ",") != "cg:5" || strings.Join(r.d.Performs, ",") != "ok:AXPress" {
		t.Fatalf("%v %v", r.d.Raised, r.d.Performs)
	}
	// Two of its title: none is taken, and a step with no plan window stops.
	r = frontless(t, "Export", "Export")
	res := r.run(waitFront("Export"), protocol.Step{Key: "return"})
	if res.OK || len(r.d.Raised) != 0 || len(r.d.Calls) != 0 {
		t.Fatalf("%s %s", jsonOf(res), r.d)
	}
	// An alert that only carries the title is never taken.
	r = frontless(t, "Export failed")
	r.d.Window("cg:5").Role = "alert"
	res = r.run(waitFront("Export"))
	if res.OK || len(r.d.Raised) != 0 {
		t.Fatal(jsonOf(res))
	}
}

func retitleRig(t *testing.T) *rig {
	r := newRig(t, policy.Config{}).notes()
	sleeps := 0
	r.clk.onSleep = func() {
		sleeps++
		switch sleeps {
		case 1:
			r.d.Window("cg:1").Title = "Notes — Edited"
		case 4:
			r.d.Window("cg:1").Active = false
			r.d.Win("cg:5", "TextEdit", "Save", 100, [4]int{0, 0, 10, 10}, true)
		}
	}
	r.models()
	return r
}

func TestWaitIgnoresRetitledFront(t *testing.T) {
	r := retitleRig(t)
	res := r.run(protocol.Step{Wait: &protocol.WaitRequest{Changed: true}})
	mustPass(t, res)
	if res.Steps[0].Changed == nil || strings.Join(res.Steps[0].Changed.Opened, ",") != "Save" || r.m.calls() != 0 {
		t.Fatalf("%s", jsonOf(res.Steps[0]))
	}
}

func TestWaitIgnoresTitleOnly(t *testing.T) {
	r := retitleRig(t)
	out, perr := r.e.WaitTool()(context.Background(), &engine.Call{}, json.RawMessage(`{"changed":true}`))
	if perr != nil {
		t.Fatal(perr)
	}
	w := out.(*protocol.WaitResult)
	if !w.Met || w.Changed == nil || strings.Join(w.Changed.Opened, ",") != "Save" || r.clk.now().IsZero() {
		t.Fatalf("%+v", w)
	}
	if ch := changed([]view.RawWindow{{Key: "a", Title: "x", Active: true}}, []view.RawWindow{{Key: "a", Title: "x*", Active: true}}); ch != nil {
		t.Fatalf("a title change counted: %+v", ch)
	}
}

func TestWaitFrontSkipsTree(t *testing.T) {
	r := newRig(t, policy.Config{})
	r.d.Win("cg:5", "TextEdit", "Save As", 100, [4]int{0, 0, 10, 10}, true).Role = "dialog"
	mustPass(t, r.run(waitFront("Save As")))
	if r.d.trees != 0 {
		t.Fatalf("read %d trees", r.d.trees)
	}
	// An alert in front stops the plan, even with an empty tree.
	r = newRig(t, policy.Config{})
	r.d.Win("cg:5", "TextEdit", "The document could not be saved", 100, [4]int{0, 0, 10, 10}, true).Role = "alert"
	r.d.Trees["cg:5"] = nil
	mustStop(t, r.run(waitFront("Save As")), 0, protocol.ErrTimeout)
}

func TestNoFrontWindowAdoptsUniqueTitle(t *testing.T) {
	r := frontless(t, "Export", "Other")
	r.d.Add("cg:5", "name", "textbox", "Name", nil, "cg:5")
	mustPass(t, r.run(waitFront("Export"), protocol.Step{Set: sel(`textbox "Name"`), Value: raw(`"a"`)}))
	if r.d.Values["name"] != "a" {
		t.Fatal(r.d.Values)
	}
	// With no window of its own, the plan stops at no focused window.
	r = frontless(t, "Export")
	res := r.run(protocol.Step{Key: "cmd+s"})
	mustStop(t, res, 0, protocol.ErrWindowNotFound)
	if !strings.Contains(res.Error.Message, "no focused window") || len(r.d.Calls) != 0 {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
}

func TestNoFrontWindowNoGuess(t *testing.T) {
	r := frontless(t, "Notes")
	res := r.run(protocol.Step{Type: str("hello")})
	mustStop(t, res, 0, protocol.ErrWindowNotFound)
	if len(r.d.Calls) != 0 {
		t.Fatal(r.d)
	}
	// A window the key before is opening is waited for.
	r = frontless(t, "Notes")
	sleeps := 0
	r.clk.onSleep = func() {
		if sleeps++; sleeps == 3 {
			r.d.Window("cg:5").Active = true
		}
	}
	mustPass(t, r.run(protocol.Step{Key: "cmd+s"}))
	if len(keyCalls(r.d)) != 1 {
		t.Fatal(r.d)
	}
}

func TestStepWaitsForLateDialog(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "open", "button", "Open", nil, "cg:1")
	opened := false
	r.d.onPerform = func(id, _ string) { opened = opened || id == "open" }
	n := 0
	r.d.AfterList = func(d *fakedesk.Desk, _ int) {
		if !opened {
			return
		}
		if n++; n == 4 {
			d.Window("cg:1").Active = false
			d.Wins = append([]view.RawWindow{{Key: "cg:6", Handle: "cg:6", App: "TextEdit", PID: 100, Title: "Open", Role: "dialog", Active: true}}, d.Wins...)
			d.Trees["cg:6"] = []view.RawNode{{Handle: "cg:6", Key: "cg:6", Parent: -1, Role: "dialog", Name: "Open"}}
		}
		if n == 7 {
			d.Add("cg:6", "ok", "button", "OK", nil, "cg:6")
		}
	}
	res := r.run(protocol.Step{Press: sel(`button "Open"`)}, protocol.Step{Press: sel(`button "OK"`)})
	mustPass(t, res)
	if strings.Join(r.d.Performs, ",") != "open:AXPress,ok:AXPress" {
		t.Fatal(r.d.Performs)
	}
}

func TestEndWaitOnlyWhenStepCanOpen(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.onDriver = func(tool string, _ map[string]any) {
		n := 0
		r.d.AfterList = func(d *fakedesk.Desk, _ int) {
			if n++; n == 3 {
				d.Window("cg:1").Active = false
				d.Wins = append(d.Wins, view.RawWindow{Key: "cg:7", Handle: "cg:7", App: "TextEdit", PID: 100, Title: "Open", Role: "dialog", Active: true})
			}
		}
	}
	res := r.run(protocol.Step{Key: "cmd+o"})
	mustPass(t, res)
	if ch := res.Steps[0].Changed; ch == nil || ch.Front != "Open" || strings.Join(ch.Opened, ",") != "Open" {
		t.Fatalf("%s", jsonOf(res.Steps[0]))
	}
	// A plain letter cannot open anything: no end wait.
	r = newRig(t, policy.Config{}).notes()
	start := r.clk.now()
	mustPass(t, r.run(protocol.Step{Key: "a"}))
	if r.clk.now() != start || r.d.trees != 0 {
		t.Fatalf("waited %s, %d trees", r.clk.now().Sub(start), r.d.trees)
	}
}

func TestNoEndWaitForInPlaceKeys(t *testing.T) {
	plain := &view.RawWindow{Role: "window"}
	dialog := &view.RawWindow{Role: "dialog"}
	for _, c := range []struct {
		s     protocol.Step
		front *view.RawWindow
		open  bool
	}{
		{protocol.Step{Key: "shift+a"}, plain, false},
		{protocol.Step{Key: "down"}, plain, false},
		{protocol.Step{Key: "shift+down"}, plain, false},
		{protocol.Step{Type: str("abc")}, plain, false},
		{protocol.Step{Key: "down"}, dialog, true},
		{protocol.Step{Key: "tab"}, plain, true},
		{protocol.Step{Key: "shift+tab"}, plain, true},
		{protocol.Step{Key: "cmd+w"}, plain, true},
		{protocol.Step{Type: str("abc\n")}, plain, true},
		{protocol.Step{Type: str("abc\t")}, plain, true},
	} {
		kind, _ := c.s.Kind()
		if canOpen(c.s, kind, c.front) != c.open {
			t.Fatalf("%s: want %v", jsonOf(c.s), c.open)
		}
	}
}

func TestFailureViewSkipsShellWindows(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.e.Observer = &fakeObserver{res: &protocol.ObserveResult{View: "v4", Outline: "w1 window \"Notes\"", Windows: []protocol.Window{
		{Ref: "w1", App: "TextEdit", Title: "Notes"}, {Ref: "w2", App: "Dock", Title: "Dock"}, {Ref: "w3", App: "Window Server", Title: "Menubar"},
	}}}
	res := r.run(protocol.Step{Press: sel(`button "Missing"`)})
	if res.OK || res.Observe == nil || len(res.Observe.Windows) != 1 || res.Observe.Outline == "" || res.Error.Observe == nil {
		t.Fatalf("%s", jsonOf(res))
	}
}

func TestFailureViewNotes(t *testing.T) {
	for _, c := range []struct{ note, want string }{
		{"outline cut: the window has more than 2500 elements or nests deeper than 64 levels; observe a narrower view", NoteReadCut},
		{"outline cut at the 6000-character budget after 10 of 90 elements; observe", NoteBudgetCut},
		{"", ""},
	} {
		r := newRig(t, policy.Config{}).notes()
		ob := &protocol.ObserveResult{View: "v1"}
		if c.note != "" {
			ob.Notes = []string{c.note}
		}
		r.e.Observer = &fakeObserver{res: ob}
		res := r.run(protocol.Step{Press: sel(`button "Missing"`)})
		got := strings.Join(res.Observe.Notes, "|")
		if got != c.want {
			t.Fatalf("%q -> %q", c.note, got)
		}
	}
}
