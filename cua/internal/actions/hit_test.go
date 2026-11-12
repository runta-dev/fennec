package actions_test

import (
	"strings"
	"testing"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/protocol"
)

func TestTextFieldClickIsHitChecked(t *testing.T) {
	d := scene()
	r := newRig(t, d, "").step(protocol.Step{Click: tgt(`textbox "Name"`)})
	wantStatus(t, r, protocol.StatusConfirmed)
	if len(d.HitCalls) != 1 || d.HitCalls[0].Node != "name" || d.HitCalls[0].Count != 1 || len(d.Performs) != 0 || len(d.Calls) != 0 {
		t.Fatalf("%s", d)
	}
	// Buttons, right clicks and fields without a window id get a plain press.
	for _, s := range []protocol.Step{
		{Click: tgt(`button "Save"`)},
		{Click: tgt(`textbox "Name"`), Button: "right"},
	} {
		d = scene()
		newRig(t, d, "").step(s)
		if len(d.HitCalls) != 0 || len(d.Performs) != 1 {
			t.Fatalf("%+v: %s", s, d)
		}
	}
	d = scene()
	d.Window("cg:10").Key = "sheet-1"
	d.Trees["sheet-1"] = d.Trees["cg:10"]
	newRig(t, d, "").step(protocol.Step{Click: tgt(`textbox "Name"`)})
	if len(d.HitCalls) != 0 || strings.Join(d.Performs, ",") != "name:AXPress" {
		t.Fatalf("without a window id: %s", d)
	}
}

func TestHitRefusalFallsBackToPress(t *testing.T) {
	for _, code := range []string{"hit_mismatch", "foreground_unavailable", "hit_check_unavailable"} {
		d := scene()
		d.HitReplies = []fakedesk.Reply{fakedesk.Refused("click", code, "click.verify_hit: refused; nothing was sent.")}
		r := newRig(t, d, "").step(protocol.Step{Click: tgt(`textbox "Name"`)})
		wantStatus(t, r, protocol.StatusUnconfirmed)
		if strings.Join(d.Performs, ",") != "name:AXPress" || !notesHave(r, "focus may not have moved") || len(d.Calls) != 0 {
			t.Fatalf("%s: %+v %s", code, r, d)
		}
	}
}

func TestRefusalThenUnsupportedPressNoPixelClick(t *testing.T) {
	d := scene()
	d.HitReplies = []fakedesk.Reply{fakedesk.Refused("click", "hit_mismatch", "click.verify_hit: another element is on top; nothing was sent.")}
	d.PerformErr["name"] = unsupported
	r := newRig(t, d, "").step(protocol.Step{Click: tgt(`textbox "Name"`)})
	wantStatus(t, r, protocol.StatusRefused)
	if !strings.Contains(r.Error.Message, "another element is on top") || !strings.Contains(r.Error.Message, "-25206") {
		t.Fatalf("both errors: %+v", r.Error)
	}
	if len(d.Calls) != 0 {
		t.Fatalf("a pixel click was sent: %s", d)
	}
}

func TestSentHitCheckedClickNotRepeated(t *testing.T) {
	d := scene()
	d.HitReplies = []fakedesk.Reply{fakedesk.Failed("click", "click.verify_hit: the click at (200, 210) failed after it was sent: posting")}
	r := newRig(t, d, "").step(protocol.Step{Click: tgt(`textbox "Name"`)})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if len(d.HitCalls) != 1 || len(d.Performs) != 0 || len(d.Calls) != 0 || !retryFalse(r) {
		t.Fatalf("%+v %s", r, d)
	}
}

func TestUnsupportedPressGetsOneHitCheckedClick(t *testing.T) {
	d := scene()
	d.PerformErr["save"] = unsupported
	r := newRig(t, d, "").step(protocol.Step{Click: tgt(`button "Save"`)})
	if r.Status == protocol.StatusRefused || len(d.HitCalls) != 1 || d.HitCalls[0].Node != "save" {
		t.Fatalf("%+v %s", r, d)
	}
	d = scene()
	d.PerformErr["save"] = unsupported
	d.HitReplies = []fakedesk.Reply{fakedesk.Refused("click", "hit_mismatch", "click.verify_hit: covered; nothing was sent.")}
	r = newRig(t, d, "").step(protocol.Step{Click: tgt(`button "Save"`)})
	wantStatus(t, r, protocol.StatusRefused)
	if len(d.HitCalls) != 1 || len(d.Calls) != 0 || !strings.Contains(r.Error.Message, "covered") || !strings.Contains(r.Error.Message, "does not support") {
		t.Fatalf("%+v %s", r.Error, d)
	}
}

func TestUnsupportedDoubleClickRetriedWithCount(t *testing.T) {
	d := scene()
	d.PerformErr["save"] = unsupported
	newRig(t, d, "").step(protocol.Step{Click: tgt(`button "Save"`), Count: 2})
	if len(d.HitCalls) != 1 || d.HitCalls[0].Count != 2 {
		t.Fatalf("%s", d)
	}
}

func TestNoRetryWithoutWindowOrLeftButton(t *testing.T) {
	d := scene()
	d.PerformErr["save:AXShowMenu"] = unsupported
	newRig(t, d, "").step(protocol.Step{Click: tgt(`button "Save"`), Button: "right"})
	if len(d.HitCalls) != 0 {
		t.Fatalf("a right click was retried: %s", d)
	}
	d = scene()
	d.Window("cg:10").Key = "sheet-1"
	d.Trees["sheet-1"] = d.Trees["cg:10"]
	d.PerformErr["save"] = unsupported
	newRig(t, d, "").step(protocol.Step{Click: tgt(`button "Save"`)})
	if len(d.HitCalls) != 0 {
		t.Fatalf("an element without a window id was retried: %s", d)
	}
}

func TestTimedOutPressNotRetried(t *testing.T) {
	d := scene()
	d.PerformErr["save"] = timedOut
	r := newRig(t, d, "").step(protocol.Step{Click: tgt(`button "Save"`)})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if len(d.HitCalls) != 0 || len(d.Calls) != 0 || !retryFalse(r) {
		t.Fatalf("%+v %s", r, d)
	}
}

func TestLeftClickClassification(t *testing.T) {
	// The left button is the default, by any spelling.
	for _, b := range []string{"", "left", "Left", " primary "} {
		d := scene()
		d.PerformErr["save"] = unsupported
		newRig(t, d, "").step(protocol.Step{Click: tgt(`button "Save"`), Button: b})
		if len(d.HitCalls) != 1 {
			t.Errorf("%q is a left click: %s", b, d)
		}
	}
	for _, b := range []string{"middle", "right"} {
		d := scene()
		d.PerformErr["save"] = unsupported
		newRig(t, d, "").step(protocol.Step{Click: tgt(`button "Save"`), Button: b})
		if len(d.HitCalls) != 0 {
			t.Errorf("%q is not a left click: %s", b, d)
		}
	}
	_ = actions.AXActionUnsupported
}
