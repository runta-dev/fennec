package plan

import (
	"context"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// selectAll says whether a cmd+a went out.
func selectAll(d *desk) bool {
	for _, c := range keyCalls(d) {
		if c.Arg("keys") == `["cmd","a"]` {
			return true
		}
	}
	return false
}

func TestChooserDetection(t *testing.T) {
	// The panel in front: its file name field lies far into its tree. The
	// name's text is selected, the line is typed without its newline, and
	// one Return submits it once Save is enabled.
	r := newRig(t, policy.Config{}).notes()
	saveAs(r, true)
	r.d.onDriver = panel(r, nil)
	res := r.run(protocol.Step{Type: str("report.txt\n")})
	mustPass(t, res)
	ty := typeCalls(r.d)
	if !selectAll(r.d) || len(ty) != 1 || ty[0].Arg("text") != "report.txt" || returns(r.d) != 1 {
		t.Fatalf("%s", r.d)
	}
	if r.d.Window("cg:5") != nil {
		t.Fatal("the panel is still open")
	}
	// The field lies past 1800 characters of the outline.
	tree, _ := r.d.Desk.Tree(context.Background(), view.RawWindow{Key: "cg:5"}, view.TreeOptions{})
	walk := r.x.Refs.Begin()
	out := view.RenderOutline(walk.Snapshot(tree), view.OutlineOptions{Budget: 1 << 20}).Text
	walk.End()
	if at := strings.Index(out, "Save As:"); at < 1800 {
		t.Fatalf("the field is at %d of the outline", at)
	}

	// A shortcut that opens the panel late: the line waits for it, through
	// a moment with no window in front, and treats it as a chooser.
	r = newRig(t, policy.Config{}).notes()
	saveAs(r, false)
	play := panel(r, nil)
	shortcut := false
	sleeps := 0
	r.d.onDriver = func(tool string, args map[string]any) {
		if tool == "hotkey" && !shortcut {
			// cmd+shift+s: nothing in front for a while.
			shortcut = true
			r.d.Window("cg:1").Active = false
			return
		}
		play(tool, args)
	}
	r.clk.onSleep = func() {
		if sleeps++; sleeps == 3 && r.d.Window("cg:5") == nil {
			play("hotkey", nil)
		}
	}
	res = r.run(protocol.Step{Key: "cmd+shift+s"}, protocol.Step{Type: str("report.txt\n")})
	mustPass(t, res)
	ty = typeCalls(r.d)
	if !selectAll(r.d) || len(ty) != 1 || ty[0].Arg("text") != "report.txt" || returns(r.d) != 1 {
		t.Fatalf("%s", r.d)
	}

	// A Return step in the panel waits for Save too.
	r = newRig(t, policy.Config{}).notes()
	saveAs(r, true)
	r.d.onDriver = panel(r, nil)
	readies := 0
	r.clk.onSleep = func() {
		if readies++; readies == 2 {
			r.d.Node("save5").States = nil
		}
	}
	mustPass(t, r.run(protocol.Step{Key: "return"}))
	if returns(r.d) != 1 || readies < 2 || r.d.trees < 2 {
		t.Fatalf("the Return did not wait for Save: %d sleeps, %d trees, %s", readies, r.d.trees, r.d)
	}

	// A large ordinary window with a "File name" field is no chooser: one
	// typing call with its newline, no select-all.
	r = newRig(t, policy.Config{}).notes()
	for i := 0; i < 40; i++ {
		r.d.Add("cg:1", "p"+itoa(i), "statictext", strings.Repeat("Paragraph text. ", 4), nil, "cg:1")
	}
	r.d.Add("cg:1", "fname", "textbox", "File name", fakedesk.Box(10, 500, 300, 20), "cg:1", "focused")
	mustPass(t, r.run(protocol.Step{Type: str("x\n")}))
	ty = typeCalls(r.d)
	if selectAll(r.d) || len(ty) != 1 || ty[0].Arg("text") != "x\n" || returns(r.d) != 0 {
		t.Fatalf("%s", r.d)
	}
}

func TestNoInputIntoUnplannedAlert(t *testing.T) {
	// The first Return raises an overwrite confirmation: no second Return,
	// and the plan stops with the alert as the current window.
	r := newRig(t, policy.Config{}).notes()
	saveAs(r, true)
	r.d.Node("save5").States = nil
	ob := &fakeObserver{res: &protocol.ObserveResult{View: "v"}}
	r.e.Observer = ob
	r.d.onDriver = func(tool string, args map[string]any) {
		if tool == "press_key" && args["key"] == "return" && r.d.Window("cg:6") == nil {
			r.d.Window("cg:5").Active = false
			r.d.Wins = append([]view.RawWindow{{Key: "cg:6", Handle: "cg:6", App: "TextEdit", PID: 100, Title: `"report.txt" already exists. Do you want to replace it?`, Role: "alert", Active: true, OwnerKey: "cg:5"}}, r.d.Wins...)
			r.d.Trees["cg:6"] = []view.RawNode{{Handle: "cg:6", Key: "cg:6", Parent: -1, Role: "alert"}}
		}
	}
	res := r.run(protocol.Step{Type: str("report.txt\n")})
	mustStop(t, res, 0, protocol.ErrFocusMismatch)
	if returns(r.d) != 1 || !strings.Contains(res.Error.Message, "already exists") {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
	if len(ob.reqs) == 0 || ob.reqs[0].Window == "" {
		t.Fatalf("no view of the alert: %+v", ob.reqs)
	}
	if e, err := r.x.Refs.Lookup(ob.reqs[0].Window); err != nil || e.WindowKey != "cg:6" {
		t.Fatalf("the current window is not the alert: %+v %v", e, err)
	}

	// A dialog the plan already read, retitled into an alert: the next
	// step reads it again and stops before sending anything.
	r = newRig(t, policy.Config{}).notes()
	r.d.onDriver = func(tool string, args map[string]any) {
		switch {
		case tool == "hotkey" && r.d.Window("cg:7") == nil:
			r.d.Window("cg:1").Active = false
			r.d.Wins = append([]view.RawWindow{{Key: "cg:7", Handle: "cg:7", App: "TextEdit", PID: 100, Title: "New Folder", Role: "dialog", Active: true}}, r.d.Wins...)
			r.d.Trees["cg:7"] = []view.RawNode{{Handle: "cg:7", Key: "cg:7", Parent: -1, Role: "dialog", Name: "New Folder"}}
		case tool == "press_key" && args["key"] == "tab":
			w := r.d.Window("cg:7")
			w.Title, w.Role = "The name is taken", "alert"
			r.d.Trees["cg:7"][0].Role = "alert"
		}
	}
	trees := 0
	res = r.run(protocol.Step{Key: "cmd+shift+n"}, protocol.Step{Key: "tab"}, protocol.Step{Key: "x"})
	mustStop(t, res, 2, protocol.ErrFocusMismatch)
	trees = r.d.trees
	if len(keyCalls(r.d)) != 2 || !strings.Contains(res.Error.Message, "The name is taken") || trees != 2 {
		t.Fatalf("%d trees, %+v %s", trees, res.Error, r.d)
	}
}

func TestTreeReadBudget(t *testing.T) {
	newFolder := func(r *rig) {
		r.d.onDriver = func(tool string, _ map[string]any) {
			if tool == "hotkey" && r.d.Window("cg:7") == nil {
				r.d.Window("cg:1").Active = false
				r.d.Wins = append([]view.RawWindow{{Key: "cg:7", Handle: "cg:7", App: "TextEdit", PID: 100, Title: "New Folder", Role: "dialog", Active: true}}, r.d.Wins...)
				r.d.Trees["cg:7"] = []view.RawNode{{Handle: "cg:7", Key: "cg:7", Parent: -1, Role: "dialog", Name: "New Folder"}}
				r.d.Add("cg:7", "fname", "textbox", "Name", fakedesk.Box(10, 10, 100, 20), "cg:7", "focused")
			}
		}
	}
	// A dialog opened mid-plan is read once before typing into it; a later
	// key in it reads nothing new.
	r := newRig(t, policy.Config{}).notes()
	newFolder(r)
	mustPass(t, r.run(protocol.Step{Key: "cmd+shift+n"}, protocol.Step{Type: str("Reports")}, protocol.Step{Key: "tab"}))
	if r.d.trees != 1 {
		t.Fatalf("%d tree reads", r.d.trees)
	}
	// A wait whose window is in front takes it without a tree read.
	r = newRig(t, policy.Config{}).notes()
	newFolder(r)
	mustPass(t, r.run(protocol.Step{Key: "cmd+shift+n"}, waitFront("New Folder")))
	if r.d.trees != 0 {
		t.Fatalf("%d tree reads", r.d.trees)
	}
	// A plan whose only step cannot open anything reads no tree.
	for _, s := range []protocol.Step{{Type: str("abc")}, {Key: "a"}} {
		r = newRig(t, policy.Config{}).notes()
		mustPass(t, r.run(s))
		if r.d.trees != 0 {
			t.Fatalf("%s: %d tree reads", jsonOf(s), r.d.trees)
		}
	}
}

func TestRetitleWaitIsEventDriven(t *testing.T) {
	const edited = "Notes — Edited"
	newTitle := func(r *rig) *int {
		seen := 0
		r.d.AfterList = func(d *fakedesk.Desk, _ int) {
			if d.Window("cg:1").Title == edited {
				seen++
			}
		}
		return &seen
	}
	// cmd+s marks the document 300 ms later; the daemon's window-change
	// wait wakes once for it.
	r := newRig(t, policy.Config{}).notes()
	seen := newTitle(r)
	var keyAt int
	r.d.onDriver = func(tool string, _ map[string]any) { keyAt = r.d.ListReads }
	waits, sleeps := 0, 0
	r.clk.onSleep = func() { sleeps++ }
	r.e.WindowChange = func(ctx context.Context, limit time.Duration, list func(context.Context) (bool, error)) (bool, error) {
		waits++
		r.clk.advance(300 * time.Millisecond)
		r.d.Window("cg:1").Title = edited
		return list(ctx)
	}
	res := r.run(protocol.Step{Key: "cmd+s"})
	mustPass(t, res)
	if waits != 1 || sleeps != 0 || *seen != 1 || r.d.ListReads-keyAt > 3 {
		t.Fatalf("%d waits, %d sleeps, new title seen %d times, %d lists after the key", waits, sleeps, *seen, r.d.ListReads-keyAt)
	}
	// Polling the list instead would read it more than three times.
	r = newRig(t, policy.Config{}).notes()
	newTitle(r)
	r.d.onDriver = func(tool string, _ map[string]any) { keyAt = r.d.ListReads }
	sleeps = 0
	r.clk.onSleep = func() {
		if sleeps++; sleeps == 3 {
			r.d.Window("cg:1").Title = edited
		}
	}
	mustPass(t, r.run(protocol.Step{Key: "cmd+s"}))
	if r.d.ListReads-keyAt <= 3 {
		t.Fatalf("polling read the list only %d times", r.d.ListReads-keyAt)
	}
}

func TestBlankFieldCheck(t *testing.T) {
	body := func(r *rig, value string) {
		r.d.Add("cg:1", "body", "textbox", "Body", fakedesk.Box(10, 300, 400, 200), "cg:1", "focused")
		r.d.Node("body").Value = &value
		r.d.Focus[100] = []*view.RawNode{r.d.Node("body")}
	}
	blank := &protocol.Recorded{Blank: true}
	for _, into := range []*protocol.Target{sel(`textbox "Body"`), nil} {
		// The field holds text now: nothing is typed.
		r := newRig(t, policy.Config{}).notes()
		body(r, "Dear Sir,")
		res := r.run(protocol.Step{Type: str("Hello"), Into: into, Recorded: blank})
		mustStop(t, res, 0, protocol.ErrExpectFailed)
		if !strings.Contains(res.Error.Message, "not empty") || len(typeCalls(r.d)) != 0 {
			t.Fatalf("%+v %s", res.Error, r.d)
		}
		// Empty as recorded: typed.
		r = newRig(t, policy.Config{}).notes()
		body(r, "")
		mustPass(t, r.run(protocol.Step{Type: str("Hello"), Into: into, Recorded: blank}))
		if len(typeCalls(r.d)) != 1 {
			t.Fatal(r.d)
		}
	}
	// A step without the recorded mark types as before.
	r := newRig(t, policy.Config{}).notes()
	body(r, "Dear Sir,")
	mustPass(t, r.run(protocol.Step{Type: str("Hello"), Into: sel(`textbox "Body"`)}))
	if len(typeCalls(r.d)) != 1 {
		t.Fatal(r.d)
	}
}
