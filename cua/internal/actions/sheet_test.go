package actions_test

import (
	"strings"
	"testing"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// sheetScene is TextEdit's "Notes" window with a save sheet that holds the
// focus: the app reports the sheet as its focused window, so the sheet is
// active and its window is not.
func sheetScene() *fakedesk.Desk {
	d := scene()
	d.Window("cg:10").Active = false
	d.Wins = append(d.Wins[:1], append([]view.RawWindow{{
		Key: "ax:100:1", Handle: "ax:100:1", App: "TextEdit", PID: 100, Role: "sheet",
		OwnerKey: "cg:10", Active: true, Bounds: &[4]int{100, 100, 400, 300},
	}}, d.Wins[1:]...)...)
	return d
}

// A sheet in front of its window (a save panel) takes the window's input:
// keys for the window go through without raising it.
func TestSheetInFrontTakesWindowInput(t *testing.T) {
	d := sheetScene()
	r := newRig(t, d, protocol.InputForeground).step(protocol.Step{Key: "escape", Window: "Notes"})
	if r.Status == protocol.StatusRefused {
		t.Fatalf("refused: %+v", r.Error)
	}
	if len(d.Raised) != 0 {
		t.Fatalf("raised %v under its own sheet", d.Raised)
	}
	if len(d.Calls) != 1 || d.Calls[0].Tool != "press_key" {
		t.Fatalf("%s", d)
	}
}

// Another app's window in front is never taken for the target's: the
// refusal names it.
func TestOtherAppInFrontNamed(t *testing.T) {
	d := scene()
	d.Window("cg:10").Active = false
	d.Window("cg:20").Active = true
	d.NoRaise = true
	r := newRig(t, d, protocol.InputForeground).step(protocol.Step{Key: "escape", Window: "Notes"})
	wantCode(t, r, protocol.ErrFocusMismatch)
	if !strings.Contains(r.Error.Message, `the front window is "Downloads" of Finder`) || strings.Contains(r.Error.Hint, "background input") {
		t.Fatalf("%+v", r.Error)
	}
	if len(d.Calls) != 0 {
		t.Fatalf("input was sent: %s", d)
	}
}

// A window's own secondary actions (Raise) run on its window ref.
func TestPerformOnWindowRef(t *testing.T) {
	d := scene()
	d.Trees["cg:20"][0].Actions = []string{"AXRaise"}
	x := newRig(t, d, protocol.InputForeground)
	ref := x.ref("cg:20", "")
	r := x.step(protocol.Step{Perform: &protocol.Target{Ref: ref}, Action: "Raise"})
	wantStatus(t, r, protocol.StatusConfirmed)
	if strings.Join(d.Raised, ",") != "cg:20" {
		t.Fatalf("raised %v, performs %v", d.Raised, d.Performs)
	}
	// Other element steps still refuse a window ref.
	r = x.step(protocol.Step{Press: &protocol.Target{Ref: ref}})
	wantCode(t, r, protocol.ErrInvalid)
}
