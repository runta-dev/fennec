package actions_test

import (
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/protocol"
)

var unsupported = &actions.AXError{Code: actions.AXActionUnsupported, Op: "AXPress"}
var timedOut = &actions.AXError{Code: actions.AXCannotComplete, Op: "AXPress"}

func TestPressFrontButtonRunsAction(t *testing.T) {
	d := scene()
	r := newRig(t, d, "").step(protocol.Step{Press: tgt(`button "Save"`)})
	wantStatus(t, r, protocol.StatusConfirmed)
	if strings.Join(d.Performs, ",") != "save:AXPress" || len(d.Calls) != 0 {
		t.Fatalf("%s", d)
	}
}

func TestPressBehindClicks(t *testing.T) {
	d := scene()
	d.Hits[[2]int{820, 510}] = fakedesk.Hit{Node: "trash", Win: "cg:20"}
	r := newRig(t, d, "").step(protocol.Step{Press: tgt(`button "Trash"`), Window: "Downloads"})
	if r.Status == protocol.StatusRefused || len(d.Performs) != 0 {
		t.Fatalf("%+v\n%s", r, d)
	}
	c := calls(d, "click")
	if len(c) != 1 || c[0].Arg("window_id") != "20" || c[0].Arg("x") != "420" || c[0].Arg("y") != "210" || c[0].Arg("delivery_mode") != "foreground" {
		t.Fatalf("%s", d)
	}
}

func TestPressRefusedActionClicks(t *testing.T) {
	d := scene()
	d.PerformErr["save:AXPress"] = unsupported
	r := newRig(t, d, "").step(protocol.Step{Press: tgt(`button "Save"`)})
	if r.Status == protocol.StatusRefused || len(calls(d, "click")) != 1 || calls(d, "click")[0].Arg("x") != "140" {
		t.Fatalf("%+v\n%s", r, d)
	}
}

func TestPressCoveredRunsAction(t *testing.T) {
	d := scene()
	// Something else is on top at the button's centre.
	d.Hits[[2]int{820, 510}] = fakedesk.Hit{Node: "name", Win: "cg:10"}
	r := newRig(t, d, "").step(protocol.Step{Press: tgt(`button "Trash"`), Window: "Downloads"})
	wantStatus(t, r, protocol.StatusConfirmed)
	if strings.Join(d.Performs, ",") != "trash:AXPress" || len(d.Calls) != 0 {
		t.Fatalf("%s", d)
	}
}

func TestPressSelectsTab(t *testing.T) {
	d := scene()
	r := newRig(t, d, "").step(protocol.Step{Press: tgt(`tab "General"`)})
	wantStatus(t, r, protocol.StatusConfirmed)
	if strings.Join(d.Selected, ",") != "tab" || len(d.Performs) != 0 || len(d.Calls) != 0 {
		t.Fatalf("%s", d)
	}
}

func TestPressSelectsTreeItem(t *testing.T) {
	d := scene()
	d.Add("cg:10", "row", "treeitem", "Projects", fakedesk.Box(10, 400, 100, 20), "cg:10")
	d.Hits[[2]int{60, 410}] = fakedesk.Hit{Node: "row", Win: "cg:10"}
	r := newRig(t, d, "").step(protocol.Step{Press: tgt(`treeitem "Projects"`)})
	wantStatus(t, r, protocol.StatusConfirmed)
	if strings.Join(d.Selected, ",") != "row" || len(d.Calls) != 0 {
		t.Fatalf("a tree item is selected even where a hit test finds it: %s", d)
	}
}

func TestPressMenuItemWithoutOpening(t *testing.T) {
	d := scene()
	d.Add("cg:10", "file", "menubaritem", "File", fakedesk.Box(50, 0, 30, 20), "cg:10")
	d.Add("cg:10", "filemenu", "menu", "File", nil, "file")
	d.Add("cg:10", "saveas", "menuitem", "Save As…", nil, "filemenu")
	r := newRig(t, d, "").step(protocol.Step{Press: tgt(`menuitem "Save As"`)})
	wantStatus(t, r, protocol.StatusConfirmed)
	if strings.Join(d.Performs, ",") != "saveas:AXPress" || len(d.Calls) != 0 {
		t.Fatalf("the menus above were opened or clicked: %s", d)
	}
}

func TestBackgroundPressRunsAction(t *testing.T) {
	d := scene()
	d.Hits[[2]int{820, 510}] = fakedesk.Hit{Node: "trash", Win: "cg:20"}
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Press: tgt(`button "Trash"`), Window: "Downloads"})
	wantStatus(t, r, protocol.StatusConfirmed)
	if strings.Join(d.Performs, ",") != "trash:AXPress" || len(d.Calls) != 0 || len(d.Raised) != 0 {
		t.Fatalf("%s", d)
	}
}

func TestBackgroundPressNoClickAfterFailure(t *testing.T) {
	d := scene()
	d.PerformErr["trash:AXPress"] = timedOut
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Press: tgt(`button "Trash"`), Window: "Downloads"})
	// A press the app does not answer counts as done, unproven: never
	// clicked after, never resent.
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if !retryFalse(r) || len(d.Calls) != 0 {
		t.Fatalf("%+v %s", r, d)
	}
	d = scene()
	d.PerformErr["trash:AXPress"] = unsupported
	r = newRig(t, d, protocol.InputBackground).step(protocol.Step{Press: tgt(`button "Trash"`), Window: "Downloads"})
	wantStatus(t, r, protocol.StatusRefused)
	if len(d.Calls) != 0 {
		t.Fatalf("clicked after the action failed: %s", d)
	}
}

func TestPressActionOrClick(t *testing.T) {
	d := scene()
	x := newRig(t, d, "")
	r := x.step(protocol.Step{Press: tgt(x.ref("cg:10", "save"))})
	if !strings.Contains(r.Evidence, "accessibility action") {
		t.Fatalf("%+v", r)
	}
	d.PerformErr["save:AXPress"] = unsupported
	r = x.step(protocol.Step{Press: tgt(x.ref("cg:10", "save"))})
	if r.How != "click" || !strings.Contains(r.Evidence, "clicked the centre") || len(calls(d, "click")) != 1 {
		t.Fatalf("%+v\n%s", r, d)
	}
}

func TestElementClickOwnWindow(t *testing.T) {
	d := scene()
	x := newRig(t, d, protocol.InputBackground)
	r := x.step(protocol.Step{Click: tgt(x.ref("cg:20", "trash"))})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	c := calls(d, "click")
	if len(c) != 1 || c[0].Arg("pid") != "200" || c[0].Arg("window_id") != "20" || c[0].Arg("x") != "420" || c[0].Arg("y") != "210" || c[0].Arg("delivery_mode") != "background" {
		t.Fatalf("%s", d)
	}
	if len(d.Raised) != 0 {
		t.Fatal("raised")
	}
}

func TestElementClickFrameCheck(t *testing.T) {
	// The element's centre lies outside its window's frame.
	d := scene()
	d.Add("cg:20", "far", "button", "Far", fakedesk.Box(2000, 2000, 20, 20), "cg:20")
	x := newRig(t, d, protocol.InputBackground)
	r := x.step(protocol.Step{Click: tgt(x.ref("cg:20", "far"))})
	wantCode(t, r, protocol.ErrTargetAmbiguous)
	// Twins the frames cannot tell apart.
	d = scene()
	d.Win("cg:21", "Finder", "Downloads", 200, [4]int{400, 300, 600, 400}, false)
	x = newRig(t, d, protocol.InputBackground)
	r = x.step(protocol.Step{Click: tgt(x.ref("cg:20", "trash"))})
	wantCode(t, r, protocol.ErrTargetAmbiguous)
	// The element's window is gone.
	d = scene()
	x = newRig(t, d, protocol.InputBackground)
	ref := x.ref("cg:20", "trash")
	d.Wins = d.Wins[:1]
	r = x.step(protocol.Step{Click: tgt(ref)})
	wantCode(t, r, protocol.ErrTargetNotFound)
	if len(d.Calls) != 0 {
		t.Fatal(d)
	}
}

func TestBackgroundElementActionsNoRaise(t *testing.T) {
	d := scene()
	d.Add("cg:20", "box", "checkbox", "Hidden files", fakedesk.Box(420, 320, 20, 20), "cg:20")
	x := newRig(t, d, protocol.InputBackground)
	x.step(protocol.Step{Press: tgt(`button "Trash"`), Window: "Downloads"})
	x.step(protocol.Step{Set: tgt(`checkbox "Hidden files"`), Window: "Downloads", Value: raw(true)})
	x.step(protocol.Step{Press: tgt(`tab "General"`), Window: "Notes"})
	if len(d.Raised) != 0 || len(d.Calls) != 0 {
		t.Fatalf("%s", d)
	}
}

func TestBackgroundClickSingleList(t *testing.T) {
	d := scene()
	x := newRig(t, d, protocol.InputBackground)
	ref := x.ref("cg:20", "trash")
	d.ListReads = 0
	x.step(protocol.Step{Click: tgt(ref)})
	if d.ListReads != 1 {
		t.Fatalf("listed windows %d times", d.ListReads)
	}
}

func TestElementClickOtherSpaceNote(t *testing.T) {
	d := scene()
	d.Window("cg:20").Space = "other"
	x := newRig(t, d, protocol.InputBackground)
	r := x.step(protocol.Step{Click: tgt(x.ref("cg:20", "trash"))})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if !notesHave(r, "another Space") || !notesHave(r, "do not resend") {
		t.Fatalf("%+v", r)
	}
}

func TestClickTargetsTopWindow(t *testing.T) {
	d := scene()
	d.Hits[[2]int{820, 510}] = fakedesk.Hit{Node: "trash", Win: "cg:20"}
	r := newRig(t, d, "").step(protocol.Step{Click: at(820, 510), Count: 2})
	if r.Status == protocol.StatusRefused {
		t.Fatalf("%+v", r.Error)
	}
	if strings.Join(d.Raised, ",") != "cg:20" {
		t.Fatalf("raised %v", d.Raised)
	}
	c := calls(d, "click")
	if len(c) != 1 || c[0].Arg("window_id") != "20" || c[0].Arg("x") != "420" || c[0].Arg("count") != "2" || c[0].Arg("delivery_mode") != "foreground" {
		t.Fatalf("%s", d)
	}
}

func TestClickDesktopFallbacks(t *testing.T) {
	d := scene()
	newRig(t, d, "").step(protocol.Step{Click: at(1500, 900)})
	if c := calls(d, "click"); len(c) != 1 || c[0].Arg("scope") != "desktop" || c[0].Arg("x") != "1500" {
		t.Fatalf("over no window: %s", d)
	}
	d = scene()
	d.Replies["click"] = []fakedesk.Reply{fakedesk.Refused("click", "window_server", "the driver has no WindowServer connection; nothing was sent"), fakedesk.OK}
	r := newRig(t, d, "").step(protocol.Step{Click: at(140, 110)})
	c := calls(d, "click")
	if len(c) != 2 || c[0].Arg("window_id") != "10" || c[1].Arg("scope") != "desktop" || r.Status == protocol.StatusRefused {
		t.Fatalf("%+v %s", r, d)
	}
}

func TestCoveredClickGoesBackground(t *testing.T) {
	d := scene()
	d.Add("cg:20", "list", "list", "Files", fakedesk.Box(400, 300, 300, 200), "cg:20")
	d.Hits[[2]int{500, 400}] = fakedesk.Hit{Node: "list", Win: "cg:20", Covered: "cg:10"}
	r := newRig(t, d, "").step(protocol.Step{Click: at(500, 400)})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	c := calls(d, "click")
	if len(c) != 1 || c[0].Arg("window_id") != "20" || c[0].Arg("delivery_mode") != "background" || len(d.Raised) != 0 {
		t.Fatalf("%s", d)
	}
	if !notesHave(r, "covered by TextEdit") {
		t.Fatalf("%+v", r)
	}
	// The hit test fails but the list shows the front app's window on top.
	d = scene()
	d.HitErr = errFocus
	r = newRig(t, d, "").step(protocol.Step{Click: at(140, 110)})
	if c := calls(d, "click"); len(c) != 1 || c[0].Arg("delivery_mode") != "foreground" || r.Status == protocol.StatusRefused {
		t.Fatalf("%s", d)
	}
}

func TestCoveredNamedWindowNoDesktopFallback(t *testing.T) {
	d := scene()
	d.NoRaise = true
	d.Replies["click"] = []fakedesk.Reply{fakedesk.Refused("click", "background_unavailable", "Background delivery is not available for target window class 'X' on this event kind (mouse_click). Retry")}
	r := newRig(t, d, "").step(protocol.Step{Click: at(500, 400), Window: "Downloads"})
	wantCode(t, r, protocol.ErrCovered)
	if c := calls(d, "click"); len(c) != 1 || c[0].Arg("scope") == "desktop" {
		t.Fatalf("%s", d)
	}
	if !strings.Contains(r.Error.Hint, "front") {
		t.Fatalf("%+v", r.Error)
	}
}

func TestForegroundPointerRaiseRules(t *testing.T) {
	// Inside the named window, under another app's window: raise it.
	d := scene()
	r := newRig(t, d, "").step(protocol.Step{Click: at(500, 400), Window: "Downloads"})
	if strings.Join(d.Raised, ",") != "cg:20" || r.Status == protocol.StatusRefused {
		t.Fatalf("%+v %s", r, d)
	}
	if c := calls(d, "click"); len(c) != 1 || c[0].Arg("delivery_mode") != "foreground" || c[0].Arg("window_id") != "20" {
		t.Fatalf("%s", d)
	}
	// Outside it, or over no window: no raise.
	for _, p := range [][2]float64{{100, 100}, {1500, 900}} {
		d = scene()
		newRig(t, d, "").step(protocol.Step{Click: at(p[0], p[1]), Window: "Downloads"})
		if len(d.Raised) != 0 {
			t.Fatalf("%v raised %v", p, d.Raised)
		}
	}
	// An untitled window of the same app on top counts as its own.
	d = scene()
	d.Win("cg:22", "Finder", "", 200, [4]int{450, 350, 100, 100}, false)
	d.Wins = append(d.Wins[2:], d.Wins[:2]...)
	newRig(t, d, "").step(protocol.Step{Click: at(500, 400), Window: "Downloads"})
	if len(d.Raised) != 0 {
		t.Fatalf("raised past its own popup: %v", d.Raised)
	}
}

func TestClickOnOpenMenuItemRunsIt(t *testing.T) {
	d := scene()
	d.Add("cg:10", "copy", "menuitem", "Copy", fakedesk.Box(60, 30, 100, 20), "cg:10", "showing")
	d.Hits[[2]int{110, 40}] = fakedesk.Hit{Node: "copy", Win: "cg:10"}
	x := newRig(t, d, "")
	r := x.step(protocol.Step{Click: at(110, 40)})
	wantStatus(t, r, protocol.StatusConfirmed)
	if strings.Join(d.Performs, ",") != "copy:AXPress" || len(d.Calls) != 0 {
		t.Fatalf("%s", d)
	}
	d.PerformErr["copy"] = unsupported
	if r := x.step(protocol.Step{Click: at(110, 40)}); r.OK {
		t.Fatalf("an item without an action reported success: %+v", r)
	}
}

func TestCoverFallbackWindowList(t *testing.T) {
	// The target app's window on top gets a foreground click.
	d := scene()
	d.HitErr = errFocus
	newRig(t, d, "").step(protocol.Step{Click: at(140, 110)})
	if c := calls(d, "click"); len(c) != 1 || c[0].Arg("delivery_mode") != "foreground" || c[0].Arg("window_id") != "10" {
		t.Fatalf("%s", d)
	}
	// Another app's window on top: a background click naming that app.
	d = scene()
	d.HitErr = errFocus
	d.Wins[0], d.Wins[1] = d.Wins[1], d.Wins[0]
	r := newRig(t, d, "").step(protocol.Step{Click: at(500, 400)})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if c := calls(d, "click"); len(c) != 1 || c[0].Arg("delivery_mode") != "background" || c[0].Arg("window_id") != "10" || !notesHave(r, "Finder") {
		t.Fatalf("%+v %s", r, d)
	}
	// No window there: the point is unchecked.
	d = scene()
	d.HitErr = errFocus
	r = newRig(t, d, "").step(protocol.Step{Click: at(1500, 900)})
	if !notesHave(r, "could not be checked") {
		t.Fatalf("%+v", r)
	}
}

func TestClickReplyCallerPoint(t *testing.T) {
	d := scene()
	r := newRig(t, d, "").step(protocol.Step{Click: winAt(10, 20, "Downloads")})
	if !strings.HasPrefix(r.Evidence, "clicked at (10, 20) in window pixels") {
		t.Fatalf("%q", r.Evidence)
	}
	if c := calls(d, "click"); len(c) != 1 || c[0].Arg("x") != "10" || c[0].Arg("y") != "20" {
		t.Fatalf("%s", d)
	}
}

func TestNegativeCoordinates(t *testing.T) {
	d := scene()
	d.Win("cg:30", "Preview", "Left", 300, [4]int{-1000, -200, 800, 600}, false)
	d.Hits[[2]int{-900, -150}] = fakedesk.Hit{Win: "cg:30"}
	r := newRig(t, d, "").step(protocol.Step{Click: at(-900, -150)})
	if r.Status == protocol.StatusRefused {
		t.Fatalf("%+v", r.Error)
	}
	if c := calls(d, "click"); len(c) != 1 || c[0].Arg("x") != "100" || c[0].Arg("y") != "50" {
		t.Fatalf("%s", d)
	}
}

func TestWholePixels(t *testing.T) {
	d := scene()
	d.Hits[[2]int{141, 110}] = fakedesk.Hit{Node: "save", Win: "cg:10"}
	newRig(t, d, "").step(protocol.Step{Click: at(140.6, 110.4)})
	c := calls(d, "click")
	if len(c) != 1 {
		t.Fatal(d)
	}
	if _, ok := c[0].Args["x"].(int); !ok || c[0].Arg("x") != "141" || c[0].Arg("y") != "110" {
		t.Fatalf("%v", c[0].Args)
	}
}

func TestClickSpacing(t *testing.T) {
	d := scene()
	x := newRig(t, d, "")
	x.step(protocol.Step{Click: at(140, 110)})
	t0 := x.clk.now()
	x.step(protocol.Step{Click: at(145, 112)})
	if el := x.clk.now().Sub(t0); el < actions.ClickSpacing {
		t.Fatalf("a second click nearby came after %s", el)
	}
	t1 := x.clk.now()
	x.step(protocol.Step{Click: at(700, 100)})
	if el := x.clk.now().Sub(t1); el >= actions.ClickSpacing {
		t.Fatalf("a click far away waited %s", el)
	}
	// A dropped call still counts.
	d = scene()
	d.Replies["click"] = []fakedesk.Reply{fakedesk.Failed("click", "no reply (the input may have been sent): EOF"), fakedesk.OK}
	x = newRig(t, d, "")
	x.step(protocol.Step{Click: at(140, 110)})
	t2 := x.clk.now()
	x.step(protocol.Step{Click: at(140, 110)})
	if el := x.clk.now().Sub(t2); el < actions.ClickSpacing {
		t.Fatalf("after a dropped click the next came after %s", el)
	}
}

func TestShotDoesNotShiftClickScale(t *testing.T) {
	d := scene()
	x := newRig(t, d, protocol.InputBackground)
	x.step(protocol.Step{Click: winAt(30, 40, "Downloads")})
	d.ScaleV = 2 // a capture at another scale in between
	x.clk.advance(time.Second)
	x.step(protocol.Step{Click: winAt(30, 40, "Downloads")})
	c := calls(d, "click")
	if len(c) != 2 || c[0].Arg("x") != c[1].Arg("x") || c[0].Arg("y") != c[1].Arg("y") || c[0].Arg("x") != "30" {
		t.Fatalf("%s", d)
	}
}

func TestWindowPixelsNeedUniqueTitle(t *testing.T) {
	d := scene()
	d.Win("cg:30", "Pages", "Doc 1", 300, [4]int{0, 700, 300, 300}, false)
	d.Win("cg:31", "Pages", "Doc 2", 300, [4]int{400, 700, 300, 300}, false)
	x := newRig(t, d, protocol.InputBackground)
	r := x.step(protocol.Step{Click: winAt(5, 5, "Doc")})
	if r.Error == nil || !strings.Contains(r.Error.Hint, "longer title") {
		t.Fatalf("%+v", r.Error)
	}
	d.Window("cg:31").Title = "Doc 1"
	r = x.step(protocol.Step{Click: winAt(5, 5, "Doc 1")})
	if r.Error == nil || !strings.Contains(r.Error.Hint, "title of its own") {
		t.Fatalf("%+v", r.Error)
	}
	if len(d.Calls) != 0 {
		t.Fatal(d)
	}
	d.Window("cg:31").Title = "Doc 2"
	if r := x.step(protocol.Step{Click: winAt(5, 5, "Doc 2")}); r.Status == protocol.StatusRefused {
		t.Fatalf("%+v", r.Error)
	}
	if r := x.step(protocol.Step{Click: at(405, 705)}); r.Status == protocol.StatusRefused {
		t.Fatalf("a screen point works: %+v", r.Error)
	}
}
