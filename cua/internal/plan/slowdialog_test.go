package plan

import (
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// A dialog that shows 1.5 s after the press and fills 1 s later gets its
// own wait once it is in front, so a step that started waiting before it
// showed still finds its target (found live in the fixture's slow scene:
// the step gave up after 2 s and turned to the model).
func TestLateDialogGetsItsOwnWait(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "open", "button", "Open Slowly", nil, "cg:1")
	var pressed time.Time
	r.d.onPerform = func(id, _ string) {
		if id == "open" {
			pressed = r.clk.now()
		}
	}
	shown, filled := false, false
	r.d.AfterList = func(d *fakedesk.Desk, _ int) {
		if pressed.IsZero() {
			return
		}
		since := r.clk.now().Sub(pressed)
		if !shown && since >= 1500*time.Millisecond {
			shown = true
			d.Window("cg:1").Active = false
			d.Wins = append([]view.RawWindow{{Key: "cg:6", Handle: "cg:6", App: "TextEdit", PID: 100, Title: "Slow Dialog", Role: "window", Active: true}}, d.Wins...)
			d.Trees["cg:6"] = []view.RawNode{{Handle: "cg:6", Key: "cg:6", Parent: -1, Role: "window", Name: "Slow Dialog"}}
		}
		if shown && !filled && since >= 2500*time.Millisecond {
			filled = true
			d.Add("cg:6", "confirm", "button", "Confirm", nil, "cg:6")
		}
	}
	res := r.run(protocol.Step{Press: sel(`button "Open Slowly"`)}, protocol.Step{Press: sel(`button "Confirm"`)})
	mustPass(t, res)
	if strings.Join(r.d.Performs, ",") != "open:AXPress,confirm:AXPress" {
		t.Fatal(r.d.Performs)
	}
}
