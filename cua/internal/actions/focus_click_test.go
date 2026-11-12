package actions_test

import (
	"strings"
	"testing"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// The driver reports a click on a text field as unverifiable; the focus
// moving into the field proves it (found live once driver replies were
// read from structuredContent).
func TestFieldClickConfirmedByFocus(t *testing.T) {
	d := scene()
	d.HitReplies = []fakedesk.Reply{fakedesk.Unverified("no visible change")}
	name := d.Node("name")
	d.Focus = map[int][]*view.RawNode{100: {name}}
	r := newRig(t, d, "").step(protocol.Step{Click: tgt(`textbox "Name"`)})
	wantStatus(t, r, protocol.StatusConfirmed)
	// Without the focus it stays unconfirmed.
	d = scene()
	d.HitReplies = []fakedesk.Reply{fakedesk.Unverified("no visible change")}
	r = newRig(t, d, "").step(protocol.Step{Click: tgt(`textbox "Name"`)})
	wantStatus(t, r, protocol.StatusUnconfirmed)
}

// A press that opens a modal alert is not answered in time: it counts as
// done, unproven, with no click after (found live: the fixture's Show
// Alert press failed the plan as app_not_responding).
func TestPressIntoModalCountsAsDone(t *testing.T) {
	d := scene()
	d.PerformErr["save:AXPress"] = timedOut
	r := newRig(t, d, "").step(protocol.Step{Press: tgt(`button "Save"`)})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if !r.OK || r.Error != nil || len(d.HitCalls) != 0 || len(d.Calls) != 0 {
		t.Fatalf("%+v %s", r, d)
	}
}

// The real driver refuses window-pixel input for a window it has not read
// in this session, marking the result isError; that refusal sent nothing,
// so the window is read and the click sent once more (found live: every
// background point click was reported done while nothing was clicked).
func TestWindowClickReadsContextFirst(t *testing.T) {
	d := scene()
	missing := fakedesk.Reply{Raw: `{"content":[{"type":"text","text":"No screenshot of window 20 in this session"}],"isError":true,"structuredContent":{"code":"screenshot_context_missing","pid":200,"window_id":20}}`}
	d.Replies["click"] = []fakedesk.Reply{missing, fakedesk.OK}
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Click: at(820, 510), Window: "Downloads"})
	var tools []string
	for _, c := range d.Calls {
		tools = append(tools, c.Tool)
	}
	if !r.OK || strings.Join(tools, ",") != "click,get_window_state,click" {
		t.Fatalf("%+v %v", r, tools)
	}
	// Any other isError result failed: it is never reported done.
	d = scene()
	d.Replies["click"] = []fakedesk.Reply{{Raw: `{"content":[{"type":"text","text":"posting failed"}],"isError":true}`}}
	r = newRig(t, d, protocol.InputBackground).step(protocol.Step{Click: at(820, 510), Window: "Downloads"})
	if r.OK || r.Status == protocol.StatusConfirmed {
		t.Fatalf("%+v", r)
	}
}

// Another app's floating panel over a named window's point is not in the
// window list; the system-wide hit test finds it, and the click goes to
// the named window behind it, never onto the panel (found live: a
// foreground click landed on the fixture's floating cover).
func TestNamedClickUnderFloatingPanel(t *testing.T) {
	d := scene()
	d.Win("cg:30", "Cover", "Panel", 300, [4]int{}, false)
	d.Hits[[2]int{500, 500}] = fakedesk.Hit{Win: "cg:30"}
	r := newRig(t, d, "").step(protocol.Step{Click: at(500, 500), Window: "Notes"})
	var clicks []string
	for _, c := range d.Calls {
		if c.Tool == "click" {
			clicks = append(clicks, c.Arg("delivery_mode")+"/"+c.Arg("window_id"))
		}
	}
	if len(clicks) != 1 || !strings.HasPrefix(clicks[0], "background/") || r.Status == protocol.StatusConfirmed {
		t.Fatalf("%v %+v", clicks, r)
	}
}

// A background point click remembers its point, so a type after it is
// refused when the focus stayed in a field that does not hold the point
// (found live: "zz" went into the Name field after a click elsewhere).
func TestBackgroundPointClickThenTypeChecksFocus(t *testing.T) {
	d := scene()
	d.Focus = map[int][]*view.RawNode{100: {d.Node("name")}}
	r := newRig(t, d, protocol.InputBackground)
	r.step(protocol.Step{Click: at(700, 500), Window: "Notes"})
	res := r.step(protocol.Step{Type: text("zz"), Window: "Notes"})
	if res.Status != protocol.StatusRefused || res.Error == nil || res.Error.Code != protocol.ErrFocusMismatch {
		t.Fatalf("%+v", res)
	}
	for _, c := range d.Calls {
		if c.Tool == "type_text" {
			t.Fatal("text was sent")
		}
	}
}
