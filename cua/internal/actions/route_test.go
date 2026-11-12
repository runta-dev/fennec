package actions_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

func TestBackgroundInputNoRaise(t *testing.T) {
	d := scene()
	x := newRig(t, d, protocol.InputBackground)
	for _, s := range []protocol.Step{
		{Key: "cmd+a", Window: "Downloads"},
		{Type: text("one\ntwo"), Window: "Downloads", AnyFocus: true},
		{Click: at(820, 510), Window: "Downloads"},
		{Scroll: at(820, 510), Window: "Downloads"},
		{Drag: at(820, 510), To: at(900, 600), Window: "Downloads"},
	} {
		if r := x.step(s); r.Status == protocol.StatusRefused {
			t.Fatalf("%+v: %+v", s, r.Error)
		}
	}
	if len(d.Raised) != 0 {
		t.Fatalf("raised %v", d.Raised)
	}
	if joined(d.Tools()) != "hotkey,type_text,press_key,type_text,click,scroll,drag" {
		t.Fatalf("%s", d)
	}
	for _, c := range d.Calls {
		if c.Arg("delivery_mode") != "background" || c.Arg("scope") != "" || c.Arg("pid") != "200" || c.Arg("window_id") != "20" {
			t.Fatalf("%s %v", c.Tool, c.Args)
		}
	}
	if c := calls(d, "click")[0]; c.Arg("x") != "420" || c.Arg("y") != "210" {
		t.Fatalf("click not in window pixels: %v", c.Args)
	}
	if c := calls(d, "drag")[0]; c.Arg("to_x") != "500" || c.Arg("to_y") != "300" {
		t.Fatalf("drag not in window pixels: %v", c.Args)
	}
}

func TestBackgroundPidReachesWindowBehind(t *testing.T) {
	d := scene()
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Key: "cmd+r", PID: 200})
	wantStatus(t, r, protocol.StatusConfirmed)
	if c := d.Calls[0]; c.Arg("pid") != "200" || c.Arg("window_id") != "20" {
		t.Fatalf("%v", c.Args)
	}
}

func TestBackgroundInputReachesOtherSpace(t *testing.T) {
	d := scene()
	d.Window("cg:20").Space = "other"
	x := newRig(t, d, protocol.InputBackground)
	r := x.step(protocol.Step{Key: "return", Window: "Downloads"})
	if r.Status == protocol.StatusRefused || d.Calls[0].Arg("window_id") != "20" {
		t.Fatalf("%+v %s", r, d)
	}
	r = x.step(protocol.Step{Type: text("x"), PID: 200, AnyFocus: true})
	if r.Status == protocol.StatusRefused {
		t.Fatalf("%+v", r.Error)
	}
}

func TestTitleTwinsAcrossSpacesRefused(t *testing.T) {
	d := scene()
	for i, space := range []string{"", "", "other", "other"} {
		w := d.Win("cg:3"+string(rune('0'+i)), "Pages", "Doc", 300, [4]int{0, 700 + i, 300, 300}, false)
		w.Space = space
	}
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Key: "return", Window: "Doc"})
	wantCode(t, r, protocol.ErrTargetAmbiguous)
	if !strings.Contains(r.Error.Message, "2 windows titled \"Doc\" are on this Space and 2 on other Spaces") || len(d.Calls) != 0 {
		t.Fatalf("%+v", r.Error)
	}
	// One on each side: the input goes to the one the title picks, with a note.
	d = scene()
	d.Win("cg:30", "Pages", "Doc", 300, [4]int{0, 700, 300, 300}, false)
	d.Win("cg:31", "Pages", "Doc", 300, [4]int{0, 700, 300, 310}, false).Space = "other"
	r = newRig(t, d, protocol.InputBackground).step(protocol.Step{Key: "return", Window: "Doc"})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if !notesHave(r, "do not send it again") || d.Calls[0].Arg("window_id") != "30" {
		t.Fatalf("%+v %s", r, d)
	}
	// Typing to twins that cannot be told apart is refused.
	d = scene()
	d.Win("cg:30", "Pages", "Doc", 300, [4]int{0, 700, 300, 300}, false)
	d.Win("cg:31", "Pages", "Doc", 300, [4]int{0, 700, 300, 300}, false).Space = "other"
	r = newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("x"), Window: "Doc", AnyFocus: true})
	wantCode(t, r, protocol.ErrTargetAmbiguous)
}

func TestUnseenWindowInputNote(t *testing.T) {
	d := scene()
	d.Window("cg:20").Hidden = "minimized"
	d.Replies["click"] = []fakedesk.Reply{fakedesk.Unverified("retry with delivery_mode foreground")}
	d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("retry with delivery_mode foreground")}
	x := newRig(t, d, protocol.InputBackground)
	r := x.step(protocol.Step{Click: at(820, 510), Window: "Downloads"})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if !notesHave(r, "may have landed") || notesHave(r, "retry with delivery_mode") {
		t.Fatalf("click: %+v", r)
	}
	r = x.step(protocol.Step{Type: text("hello"), Window: "Downloads", AnyFocus: true})
	if !notesHave(r, "confirm the text before Return or Send") {
		t.Fatalf("type: %+v", r)
	}
	// A covered but visible window passes the driver's note on.
	d = scene()
	d.Replies["click"] = []fakedesk.Reply{fakedesk.Unverified("the window is covered")}
	r = newRig(t, d, protocol.InputBackground).step(protocol.Step{Click: at(820, 510), Window: "Downloads"})
	if len(r.Notes) != 1 || r.Notes[0] != "the window is covered" {
		t.Fatalf("%+v", r.Notes)
	}
}

func TestUnseenWindowHint(t *testing.T) {
	d := scene()
	d.Window("cg:20").Space = "other"
	d.Replies["press_key"] = []fakedesk.Reply{{Raw: `{"effect":"unverifiable","escalation":{"recommended":"foreground","reason":"retry in the foreground"}}`}}
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Key: "return", Window: "Downloads"})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if !r.OK || r.Error != nil || !notesHave(r, "do not resend") || notesHave(r, "foreground") {
		t.Fatalf("%+v", r)
	}
}

func TestAppTargetWithoutWindowRefused(t *testing.T) {
	for _, input := range []string{protocol.InputForeground, protocol.InputBackground, protocol.InputAuto} {
		d := scene()
		r := newRig(t, d, input).step(protocol.Step{Key: "cmd+n", App: "Mail"})
		wantStatus(t, r, protocol.StatusRefused)
		if !strings.Contains(r.Error.Message, "has no window") || !strings.Contains(r.Error.Message, "nothing was sent") {
			t.Fatalf("%s: %+v", input, r.Error)
		}
		wantNoInput(t, d)
	}
	d := scene()
	if r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Key: "cmd+n", App: "finder"}); r.Status == protocol.StatusRefused || d.Calls[0].Arg("pid") != "200" {
		t.Fatalf("%+v %s", r, d)
	}
}

func TestForegroundRaisesNamedWindow(t *testing.T) {
	d := scene()
	x := newRig(t, d, protocol.InputForeground)
	wantStatus(t, x.step(protocol.Step{Key: "cmd+s", Window: "Downloads"}), protocol.StatusConfirmed)
	if strings.Join(d.Raised, ",") != "cg:20" || d.Calls[0].Arg("scope") != "desktop" {
		t.Fatalf("%s", d)
	}
}

func TestForegroundUnfrontedSendsNothing(t *testing.T) {
	d := scene()
	d.NoRaise = true
	r := newRig(t, d, protocol.InputForeground).step(protocol.Step{Key: "cmd+s", Window: "Downloads"})
	wantCode(t, r, protocol.ErrFocusMismatch)
	if !strings.Contains(r.Error.Message, "not frontmost") || !strings.Contains(r.Error.Message, `"Notes"`) || len(d.Calls) != 0 {
		t.Fatalf("%+v %s", r.Error, d)
	}
}

func TestFocusCloseActiveTwin(t *testing.T) {
	d := scene()
	d.Window("cg:10").Active = false
	d.Win("cg:40", "TextEdit", "Untitled", 100, [4]int{0, 0, 100, 100}, false)
	d.Win("cg:41", "TextEdit", "Untitled", 100, [4]int{10, 10, 100, 100}, true)
	x := newRig(t, d, "")
	r := x.step(protocol.Step{Focus: "Untitled"})
	wantStatus(t, r, protocol.StatusConfirmed)
	if len(d.Raised) != 0 {
		t.Fatalf("focus moved off the active twin: %v", d.Raised)
	}
	r = x.step(protocol.Step{Close: "Untitled"})
	wantStatus(t, r, protocol.StatusConfirmed)
	if strings.Join(d.Closed, ",") != "cg:41" {
		t.Fatalf("closed %v", d.Closed)
	}
}

func TestFocusRejectsMalformedRef(t *testing.T) {
	d := scene()
	x := newRig(t, d, "")
	for _, name := range []string{"e5", "   "} {
		r := x.step(protocol.Step{Focus: name})
		wantCode(t, r, protocol.ErrInvalid)
	}
	wantNoInput(t, d)
}

func TestLaunchStaysBackground(t *testing.T) {
	d := scene()
	r := newRig(t, d, "").step(protocol.Step{Launch: "Calculator"})
	wantStatus(t, r, protocol.StatusConfirmed)
	if c := d.Calls; len(c) != 1 || c[0].Tool != "launch_app" || c[0].Arg("name") != "Calculator" || len(d.Raised) != 0 {
		t.Fatalf("%s", d)
	}
	if !notesHave(r, "stays in the background") {
		t.Fatalf("%+v", r)
	}
}

func TestLaunchArgsNewInstanceUrlsToRunning(t *testing.T) {
	d := scene()
	x := newRig(t, d, "")
	x.step(protocol.Step{Launch: "Google Chrome", Args: []string{"--incognito", "https://a.example"}})
	x.step(protocol.Step{Launch: "Google Chrome", Args: []string{"https://b.example"}, URL: "https://c.example"})
	c := calls(d, "launch_app")
	if c[0].Arg("creates_new_application_instance") != "true" || c[0].Arg("additional_arguments") != `["--incognito","https://a.example"]` {
		t.Fatalf("an option starts a new instance: %v", c[0].Args)
	}
	if c[1].Arg("creates_new_application_instance") != "" || c[1].Arg("urls") != `["https://b.example","https://c.example"]` {
		t.Fatalf("documents go to the running app: %v", c[1].Args)
	}
}

func TestLaunchRetryByBundleName(t *testing.T) {
	d := scene()
	d.Win("cg:50", "Google Chrome", "New Tab - Chrome", 500, [4]int{0, 700, 300, 300}, false)
	d.Replies["launch_app"] = []fakedesk.Reply{fakedesk.Refused("launch_app", "not_found", "No installed macOS app found for name 'Chrome'"), fakedesk.OK}
	r := newRig(t, d, "").step(protocol.Step{Launch: "Chrome", URL: "https://a.example"})
	wantStatus(t, r, protocol.StatusConfirmed)
	c := calls(d, "launch_app")
	if len(c) != 2 || c[1].Arg("name") != "Google Chrome" || c[1].Arg("urls") != `["https://a.example"]` {
		t.Fatalf("%s", d)
	}
}

func TestSecurityPromptRefused(t *testing.T) {
	d := scene()
	d.Win("cg:60", "SecurityAgent", "Password", 600, [4]int{1000, 0, 300, 200}, false)
	d.Add("cg:60", "allow", "button", "Allow", fakedesk.Box(1100, 100, 60, 20), "cg:60")
	d.Hits[[2]int{1130, 110}] = fakedesk.Hit{Node: "allow", Win: "cg:60"}
	x := newRig(t, d, "")
	for _, s := range []protocol.Step{
		{Click: at(1130, 110)},
		{Click: tgt(`button "Allow"`), Window: "Password"},
		{Press: tgt(`button "Allow"`), Window: "Password"},
		{Key: "return", Window: "Password"},
		{Type: text("hunter2"), Window: "Password"},
	} {
		r := x.step(s)
		wantCode(t, r, protocol.ErrSecurityPrompt)
		if !strings.Contains(r.Error.Message, "nothing was sent") {
			t.Fatalf("%+v", r.Error)
		}
	}
	wantNoInput(t, d)
}

func TestScrollAtPointOrFrontWindow(t *testing.T) {
	d := scene()
	x := newRig(t, d, "")
	x.step(protocol.Step{Scroll: at(820, 510)})
	c := calls(d, "scroll")
	if len(c) != 1 || c[0].Arg("amount") != "3" || c[0].Arg("direction") != "down" || c[0].Arg("x") != "420" || c[0].Arg("window_id") != "20" {
		t.Fatalf("%s", d)
	}
	x.step(protocol.Step{Scroll: tgt(x.ref("cg:10", "")), Direction: "up", Amount: 5})
	c = calls(d, "scroll")
	if len(c) != 2 || c[1].Arg("x") != "" || c[1].Arg("window_id") != "10" || c[1].Arg("pid") != "100" || c[1].Arg("amount") != "5" {
		t.Fatalf("without a point, scroll keys to the window: %s", d)
	}
	if r := x.step(protocol.Step{Scroll: at(1, 1), Direction: "sideways"}); r.Status != protocol.StatusRefused {
		t.Fatalf("%+v", r)
	}
}

func TestDragTwoPoints(t *testing.T) {
	d := scene()
	r := newRig(t, d, "").step(protocol.Step{Drag: at(100, 100), To: at(300, 250)})
	if r.Status == protocol.StatusRefused {
		t.Fatalf("%+v", r.Error)
	}
	c := calls(d, "drag")
	if len(c) != 1 || c[0].Arg("from_x") != "100" || c[0].Arg("from_y") != "100" || c[0].Arg("to_x") != "300" || c[0].Arg("to_y") != "250" {
		t.Fatalf("%s", d)
	}
	if r := newRig(t, d, "").step(protocol.Step{Drag: at(1, 1)}); r.Status != protocol.StatusRefused {
		t.Fatalf("a drag without its end: %+v", r)
	}
}

func TestWaitWindowChange(t *testing.T) {
	d := scene()
	clk := &clock{t: time.Unix(0, 0)}
	base, _ := d.Windows(context.Background())
	d.AfterList = func(d *fakedesk.Desk, n int) {
		if n == 3 {
			d.Win("cg:70", "TextEdit", "Save As", 100, [4]int{0, 0, 10, 10}, false)
		}
	}
	w := actions.WindowWait{List: d.Windows, Now: clk.now, Sleep: clk.sleep}
	changed, ws, err := w.Wait(context.Background(), base, 2*time.Second)
	if err != nil || !changed || len(ws) != 3 {
		t.Fatalf("%v %v %d", changed, err, len(ws))
	}
	changed, ws, _ = w.Wait(context.Background(), ws, time.Second)
	if changed || len(ws) != 3 {
		t.Fatalf("unchanged: %v %d", changed, len(ws))
	}
}

func TestWaitTimeoutCountsListReads(t *testing.T) {
	d := scene()
	clk := &clock{t: time.Unix(0, 0)}
	base, _ := d.Windows(context.Background())
	// Each list read takes 400 ms of wall time.
	slow := func(ctx context.Context) ([]view.RawWindow, error) {
		clk.advance(400 * time.Millisecond)
		return d.Windows(ctx)
	}
	w := actions.WindowWait{List: slow, Now: clk.now, Sleep: clk.sleep}
	start := clk.now()
	changed, _, _ := w.Wait(context.Background(), base, time.Second)
	if el := clk.now().Sub(start); changed || el > 1500*time.Millisecond {
		t.Fatalf("waited %s", el)
	}
	reads := d.ListReads
	if _, _, _ = w.Wait(context.Background(), base, 0); d.ListReads != reads+1 {
		t.Fatalf("a zero timeout read %d times", d.ListReads-reads)
	}
	start = clk.now()
	w.Wait(context.Background(), base, time.Hour)
	if el := clk.now().Sub(start); el > actions.MaxWindowWait+time.Second {
		t.Fatalf("the cap: %s", el)
	}
}
