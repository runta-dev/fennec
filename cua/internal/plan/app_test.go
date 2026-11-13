package plan

import (
	"context"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

func launches(d *desk) int {
	n := 0
	for _, c := range d.Calls {
		if c.Tool == "launch_app" {
			n++
		}
	}
	return n
}

func TestAppStepReusesRunningAppWindow(t *testing.T) {
	r := newRig(t, policy.Config{})
	r.d.Win("cg:9", "Safari", "Start Page", 900, [4]int{0, 0, 800, 600}, true)
	r.d.Win("cg:2", "Mail", "All Inboxes – 3 messages", 200, [4]int{0, 0, 900, 700}, false)
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+n"})
	mustPass(t, res)
	if launches(r.d) != 0 {
		t.Fatalf("a running app was launched again: %s", r.d)
	}
	if !strings.Contains(res.Steps[0].Evidence, "All Inboxes") || res.Steps[0].Changed == nil || res.Steps[0].Changed.Front != "All Inboxes – 3 messages" {
		t.Fatalf("the result does not name the window it took: %s", jsonOf(res.Steps[0]))
	}
	if strings.Join(r.d.Raised, ",") != "cg:2" {
		t.Fatalf("the window should come to the front once: %v", r.d.Raised)
	}
}

func TestAppStepPrefersOwningAppOverTitleMention(t *testing.T) {
	r := newRig(t, policy.Config{Input: "background"})
	r.d.Win("cg:9", "Safari", "Gmail - Inbox", 900, [4]int{0, 0, 800, 600}, true)
	r.d.Win("cg:2", "Mail", "Inbox", 200, [4]int{0, 0, 900, 700}, false)
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+n"})
	mustPass(t, res)
	if launches(r.d) != 0 || !strings.Contains(res.Steps[0].Evidence, `"Inbox" of Mail`) {
		t.Fatalf("%s %s", jsonOf(res.Steps[0]), r.d)
	}
	if k := r.d.Calls[len(r.d.Calls)-1]; k.Arg("pid") != "200" {
		t.Fatalf("the key went to %s, not Mail: %s", k.Arg("pid"), r.d)
	}
	// With no app of that name, a title holding the name as a whole word
	// counts; a name inside a longer word never does.
	r = newRig(t, policy.Config{})
	r.d.Win("cg:3", "Preview", "Notesy draft", 300, [4]int{0, 0, 100, 100}, true)
	r.d.Win("cg:4", "Preview", "My Notes", 300, [4]int{0, 0, 100, 100}, false)
	res = r.run(protocol.Step{Launch: "Notes"})
	mustPass(t, res)
	if launches(r.d) != 0 || !strings.Contains(res.Steps[0].Evidence, "My Notes") {
		t.Fatalf("%s", jsonOf(res.Steps[0]))
	}
}

// launchAdds makes the fake launch open a window on a later listing.
func launchAdds(r *rig, after int, add func(d *fakedesk.Desk)) {
	launched := false
	r.d.onDriver = func(tool string, _ map[string]any) {
		if tool == "launch_app" {
			launched = true
		}
	}
	n := 0
	r.d.AfterList = func(d *fakedesk.Desk, _ int) {
		if !launched {
			return
		}
		n++
		if n == after {
			add(d)
		}
	}
}

func TestAppStepTakesLaunchedWindow(t *testing.T) {
	r := newRig(t, policy.Config{})
	r.d.Win("cg:9", "Finder", "Desktop", 50, [4]int{0, 0, 800, 600}, true)
	r.d.Replies["launch_app"] = []fakedesk.Reply{{Raw: `{"pid":300}`}}
	launchAdds(r, 3, func(d *fakedesk.Desk) {
		d.Wins = append([]view.RawWindow{{Key: "cg:7", Handle: "cg:7", App: "Visual Studio Code", PID: 300, Title: "Welcome", Role: "window", Active: true}}, d.Wins...)
		d.Wins[1].Active = false
	})
	res := r.run(protocol.Step{Launch: "Code"}, protocol.Step{Key: "cmd+n"})
	mustPass(t, res)
	if launches(r.d) != 1 || !strings.Contains(res.Steps[0].Evidence, `"Welcome"`) {
		t.Fatalf("%s %s", jsonOf(res.Steps[0]), r.d)
	}
}

func TestAppStepIgnoresRetitledForeignWindow(t *testing.T) {
	r := newRig(t, policy.Config{})
	r.d.Win("cg:4", "TextEdit", "Untitled", 400, [4]int{0, 0, 800, 600}, true)
	r.d.Replies["launch_app"] = []fakedesk.Reply{{Raw: `{"pid":300}`}}
	launchAdds(r, 1, func(d *fakedesk.Desk) { d.Window("cg:4").Title = "Calculator notes" })
	launchAdds2 := r.d.AfterList
	n := 0
	r.d.AfterList = func(d *fakedesk.Desk, k int) {
		launchAdds2(d, k)
		if d.Window("cg:4").Title == "Calculator notes" {
			n++
			if n == 3 {
				d.Wins = append(d.Wins, view.RawWindow{Key: "cg:8", Handle: "cg:8", App: "Calculator", PID: 300, Title: "Calculator", Role: "window"})
			}
		}
	}
	res := r.run(protocol.Step{Launch: "Calculator"})
	mustPass(t, res)
	if r.e.X.Refs == nil || !strings.Contains(res.Steps[0].Evidence, `window "Calculator"`) {
		t.Fatalf("took the wrong window: %s", jsonOf(res.Steps[0]))
	}
}

func TestAppStepFocusesBackgroundLaunch(t *testing.T) {
	// The launch stayed behind: the new window is fronted once, even
	// though the list marks it active.
	r := newRig(t, policy.Config{})
	r.d.Win("cg:9", "Finder", "Desktop", 50, [4]int{0, 0, 800, 600}, false)
	r.d.Replies["launch_app"] = []fakedesk.Reply{{Raw: `{"pid":300,"background":true}`}}
	launchAdds(r, 1, func(d *fakedesk.Desk) {
		d.Wins = append(d.Wins, view.RawWindow{Key: "cg:7", Handle: "cg:7", App: "Calculator", PID: 300, Title: "Calculator", Role: "window", Active: true})
	})
	mustPass(t, r.run(protocol.Step{Launch: "Calculator"}))
	if strings.Join(r.d.Raised, ",") != "cg:7" {
		t.Fatalf("raised %v", r.d.Raised)
	}
	// A launch that came up in front makes no extra focus call.
	r = newRig(t, policy.Config{})
	r.d.Replies["launch_app"] = []fakedesk.Reply{{Raw: `{"pid":300}`}}
	launchAdds(r, 1, func(d *fakedesk.Desk) {
		d.Wins = append(d.Wins, view.RawWindow{Key: "cg:7", Handle: "cg:7", App: "Calculator", PID: 300, Title: "Calculator", Role: "window", Active: true})
	})
	mustPass(t, r.run(protocol.Step{Launch: "Calculator"}))
	if len(r.d.Raised) != 0 {
		t.Fatalf("raised %v", r.d.Raised)
	}
	// An app already running with its window in front: no focus call.
	r = newRig(t, policy.Config{})
	r.d.Win("cg:7", "Calculator", "Calculator", 300, [4]int{0, 0, 200, 300}, true)
	mustPass(t, r.run(protocol.Step{Launch: "Calculator"}))
	if len(r.d.Raised) != 0 || launches(r.d) != 0 {
		t.Fatalf("%s", r.d)
	}
}

func TestLaunchStepFrontsWindow(t *testing.T) {
	r := newRig(t, policy.Config{})
	r.d.Win("cg:9", "Finder", "Desktop", 50, [4]int{0, 0, 800, 600}, true)
	r.d.Replies["launch_app"] = []fakedesk.Reply{{Raw: `{"pid":300}`}}
	launchAdds(r, 1, func(d *fakedesk.Desk) {
		d.Wins = append(d.Wins, view.RawWindow{Key: "cg:7", Handle: "cg:7", App: "Calculator", PID: 300, Title: "Calculator", Role: "window"})
	})
	res := r.run(protocol.Step{Launch: "Calculator"})
	mustPass(t, res)
	if strings.Join(r.d.Raised, ",") != "cg:7" || !hasNote(res.Steps[0].Notes, "fronted by raising the window") {
		t.Fatalf("%v %s", r.d.Raised, jsonOf(res.Steps[0]))
	}
	// Under background input nothing is fronted.
	r = newRig(t, policy.Config{Input: "background"})
	r.d.Win("cg:9", "Finder", "Desktop", 50, [4]int{0, 0, 800, 600}, true)
	r.d.Replies["launch_app"] = []fakedesk.Reply{{Raw: `{"pid":300,"background":true}`}}
	launchAdds(r, 1, func(d *fakedesk.Desk) {
		d.Wins = append(d.Wins, view.RawWindow{Key: "cg:7", Handle: "cg:7", App: "Calculator", PID: 300, Title: "Calculator", Role: "window"})
	})
	mustPass(t, r.run(protocol.Step{Launch: "Calculator"}))
	if len(r.d.Raised) != 0 {
		t.Fatalf("raised %v", r.d.Raised)
	}
	// A launch that stays behind (cua.getApp) fronts nothing under
	// foreground input either.
	r = newRig(t, policy.Config{})
	r.d.Win("cg:9", "Finder", "Desktop", 50, [4]int{0, 0, 800, 600}, true)
	r.d.Replies["launch_app"] = []fakedesk.Reply{{Raw: `{"pid":300,"background":true}`}}
	launchAdds(r, 1, func(d *fakedesk.Desk) {
		d.Wins = append(d.Wins, view.RawWindow{Key: "cg:7", Handle: "cg:7", App: "Calculator", PID: 300, Title: "Calculator", Role: "window"})
	})
	mustPass(t, r.run(protocol.Step{Launch: "Calculator", Behind: true}))
	if len(r.d.Raised) != 0 {
		t.Fatalf("raised %v", r.d.Raised)
	}
}

func TestAppStepEscalatesOnStaleApp(t *testing.T) {
	r := newRig(t, policy.Config{})
	r.d.Win("cg:2", "Mail", "Inbox", 200, [4]int{0, 0, 900, 700}, true).Stale = true
	r.d.Win("cg:3", "Mail", "Draft", 200, [4]int{0, 0, 900, 700}, false).Stale = true
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+n"})
	mustStop(t, res, 0, protocol.ErrNotResponding)
	if !strings.Contains(res.Error.Message, `"Inbox" (pid 200)`) || !strings.Contains(res.Error.Hint, `"Draft" (pid 200)`) {
		t.Fatalf("%+v", res.Error)
	}
	if len(r.d.Calls) != 0 {
		t.Fatalf("an unresponsive app got input or a second launch: %s", r.d)
	}
}

func TestStaleAppEscalates(t *testing.T) {
	r := newRig(t, policy.Config{})
	r.d.Win("cg:2", "Mail", "Inbox", 200, [4]int{0, 0, 900, 700}, false).Stale = true
	r.d.Win("cg:9", "Finder", "Desktop", 50, [4]int{0, 0, 800, 600}, true)
	res := r.run(protocol.Step{Launch: "Mail"})
	mustStop(t, res, 0, protocol.ErrNotResponding)
	if launches(r.d) != 0 {
		t.Fatal(r.d)
	}
	// A front window that stops answering is waited for, then escalates.
	r = newRig(t, policy.Config{}).notes()
	r.d.Win("cg:5", "Hang", "Busy", 500, [4]int{0, 0, 10, 10}, false)
	r.d.onPerform = func(id, _ string) {
		for i := range r.d.Wins {
			r.d.Wins[i].Active = r.d.Wins[i].Key == "cg:5"
		}
		r.d.Window("cg:5").Stale = true
	}
	start := r.clk.now()
	res = r.run(protocol.Step{Press: sel(`button "Save"`)}, protocol.Step{Key: "cmd+s"})
	mustStop(t, res, 1, protocol.ErrNotResponding)
	if waited := r.clk.now().Sub(start); waited < HungWait || waited > HungWait+time.Second {
		t.Fatalf("waited %s", waited)
	}
}

func TestAppStepURLStaleMatchNotPrefix(t *testing.T) {
	r := newRig(t, policy.Config{})
	r.d.Win("cg:2", "Chrome", "Inbox", 200, [4]int{0, 0, 900, 700}, true).Stale = true
	res := r.run(protocol.Step{Launch: "Google Chrome", URL: "https://example.com"})
	mustStop(t, res, 0, protocol.ErrNotResponding)
	if launches(r.d) != 0 {
		t.Fatal(r.d)
	}
	// A stale Safari does not block Safari Technology Preview.
	r = newRig(t, policy.Config{})
	r.d.Win("cg:2", "Safari", "Inbox", 200, [4]int{0, 0, 900, 700}, false).Stale = true
	r.d.Win("cg:3", "Safari Technology Preview", "Start", 210, [4]int{0, 0, 900, 700}, true)
	res = r.run(protocol.Step{Launch: "Safari Technology Preview", URL: "https://example.com"})
	mustPass(t, res)
	if launches(r.d) != 1 {
		t.Fatalf("%s", r.d)
	}
	if c := r.d.Calls[0]; !strings.Contains(c.Arg("urls"), "https://example.com") {
		t.Fatalf("the url went nowhere: %s", r.d)
	}
}

func TestLaunchRefusedByPolicy(t *testing.T) {
	r := newRig(t, policy.Config{Allowlist: []string{"TextEdit"}})
	res := r.run(protocol.Step{Launch: "Calculator"})
	mustStop(t, res, 0, protocol.ErrNotAllowed)
	if len(r.d.Calls) != 0 {
		t.Fatalf("an app outside the allowlist was started: %s", r.d)
	}
	// A launch the user declines in confirm mode fails at once, with no
	// wait for a window.
	r = newRig(t, policy.Config{Confirm: "every"})
	r.p.Gate.Confirmer = policy.DenyAll{}
	start := time.Now()
	res2, perr := r.e.Run(context.Background(), &engine.Call{}, protocol.ActRequest{Steps: []protocol.Step{{Launch: "Calculator"}}}, engine.PlanOptions{})
	if res2 != nil || perr == nil || perr.Code != protocol.ErrDeclined || time.Since(start) > 2*time.Second || len(r.d.Calls) != 0 {
		t.Fatalf("%+v %+v %s", res2, perr, r.d)
	}
}
