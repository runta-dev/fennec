package policy_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

func desk() *fakedesk.Desk {
	d := fakedesk.New()
	d.Win("cg:10", "TextEdit", "Notes", 100, [4]int{0, 0, 800, 600}, true)
	d.Win("cg:20", "Finder", "Downloads", 200, [4]int{400, 300, 600, 400}, false)
	return d
}

func executor(t *testing.T, d *fakedesk.Desk, c policy.Config) *actions.Executor {
	t.Helper()
	p, err := policy.New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Activity.Idle = func() time.Duration { return time.Hour }
	return &actions.Executor{B: d, Refs: view.NewRegistry(0), Policy: p, Sleep: func(context.Context, time.Duration) error { return nil }}
}

func run(x *actions.Executor, s protocol.Step) protocol.StepResult {
	return x.Step(context.Background(), actions.Request{}, s, 0)
}

func TestAutoExplicitForeground(t *testing.T) {
	// Under auto an explicit foreground key needs no window.
	d := desk()
	r := run(executor(t, d, policy.Config{Input: protocol.InputAuto}), protocol.Step{Key: "cmd+space", Input: protocol.InputForeground})
	if r.Status != protocol.StatusConfirmed || r.Input != protocol.InputForeground || len(d.Calls) != 1 || d.Calls[0].Arg("scope") != "desktop" {
		t.Fatalf("%+v %s", r, d)
	}
	// The allowlist judges it by the front window.
	d = desk()
	r = run(executor(t, d, policy.Config{Input: protocol.InputAuto, Allowlist: []string{"Finder"}}), protocol.Step{Key: "cmd+q", Input: protocol.InputForeground})
	if r.Error == nil || r.Error.Code != protocol.ErrNotAllowed || len(d.Calls) != 0 {
		t.Fatalf("%+v %s", r.Error, d)
	}
	// Plain background input refuses an explicit foreground request.
	d = desk()
	r = run(executor(t, d, policy.Config{Input: protocol.InputBackground}), protocol.Step{Key: "cmd+q", Input: protocol.InputForeground, Window: "Notes"})
	if r.Status != protocol.StatusRefused || len(d.Calls) != 0 {
		t.Fatalf("%+v", r)
	}
	// Without a request auto escalates a refusal raised before sending: here
	// a background key with no target window.
	d = desk()
	r = run(executor(t, d, policy.Config{Input: protocol.InputAuto}), protocol.Step{Key: "cmd+space"})
	if r.Status != protocol.StatusConfirmed || r.Input != protocol.InputForeground || len(d.Calls) != 1 || !strings.Contains(strings.Join(r.Notes, " "), "ran in the foreground") {
		t.Fatalf("%+v %s", r, d)
	}
}

func TestAutoNoForegroundWithoutWindow(t *testing.T) {
	d := desk()
	r := run(executor(t, d, policy.Config{Input: protocol.InputAuto}), protocol.Step{Key: "cmd+n", App: "Mail"})
	if r.Status != protocol.StatusRefused || len(d.Calls) != 0 || len(d.Raised) != 0 {
		t.Fatalf("a key for an app without a window reached the front app: %+v %s", r, d)
	}
}

func TestAllowlistGatesNamedTargetNotCover(t *testing.T) {
	// Finder's window covers the point in Notes, but the step names Notes.
	d := desk()
	x := executor(t, d, policy.Config{Input: protocol.InputBackground, Allowlist: []string{"TextEdit"}})
	r := run(x, protocol.Step{Click: &protocol.Target{Point: &protocol.Point{X: 500, Y: 400}}, Window: "Notes"})
	if r.Status == protocol.StatusRefused {
		t.Fatalf("%+v", r.Error)
	}
	// An unnamed click at a point over only Finder is refused.
	r = run(x, protocol.Step{Click: &protocol.Target{Point: &protocol.Point{X: 900, Y: 650}}})
	if r.Error == nil || r.Error.Code != protocol.ErrNotAllowed {
		t.Fatalf("%+v", r)
	}
	if len(d.Calls) != 1 {
		t.Fatal(d)
	}
	// Errors list only admitted windows.
	r = run(x, protocol.Step{Focus: "Downloads"})
	if r.Error == nil || r.Error.Code != protocol.ErrWindowNotFound {
		t.Fatalf("%+v", r.Error)
	}
	if strings.Contains(r.Error.Hint, "Finder") {
		t.Fatalf("the hint names a window outside the allowlist: %+v", r.Error)
	}
}

func TestWaitComparesAdmittedOnly(t *testing.T) {
	d := desk()
	allow := policy.NewAllowlist([]string{"TextEdit"})
	base, _ := d.Windows(context.Background())
	d.AfterList = func(d *fakedesk.Desk, n int) {
		if n == 2 {
			d.Win("cg:30", "Mail", "Inbox", 300, [4]int{0, 0, 10, 10}, false)
		}
	}
	w := actions.WindowWait{List: d.Windows, Filter: allow.Filter, Sleep: func(context.Context, time.Duration) error { return nil }}
	changed, ws, err := w.Wait(context.Background(), base, 300*time.Millisecond)
	if err != nil || changed {
		t.Fatalf("an unadmitted window counted as a change: %v %v", changed, err)
	}
	for _, x := range ws {
		if x.App != "TextEdit" {
			t.Fatalf("%+v", ws)
		}
	}
}

func TestPlansAndScriptsCannotReachSafety(t *testing.T) {
	banned := []string{"stop", "resume", "allowlist", "allow", "confirm", "locked", "policy", "safety"}
	check := func(what, name string) {
		n := strings.ToLower(name)
		for _, b := range banned {
			if strings.Contains(n, b) {
				t.Errorf("%s %s reaches the safety layer", what, name)
			}
		}
	}
	st := reflect.TypeOf(protocol.Step{})
	for i := 0; i < st.NumField(); i++ {
		check("step field", st.Field(i).Tag.Get("json"))
	}
	for _, req := range []any{protocol.ActRequest{}, protocol.RunRequest{}, protocol.FlowRequest{}, protocol.ObserveRequest{}, protocol.WaitRequest{}} {
		rt := reflect.TypeOf(req)
		for i := 0; i < rt.NumField(); i++ {
			check(rt.Name()+" field", rt.Field(i).Tag.Get("json"))
		}
	}
	bt := reflect.TypeOf((*actions.Backend)(nil)).Elem()
	for i := 0; i < bt.NumMethod(); i++ {
		if m := bt.Method(i).Name; m != "LockedDelivery" && m != "ScreenLocked" {
			check("backend method", m)
		}
	}
	// A step's input only narrows: no request widens background to foreground.
	if _, perr := policy.StepMode(protocol.InputBackground, protocol.InputForeground); perr == nil {
		t.Error("a step widened the input mode")
	}
}
