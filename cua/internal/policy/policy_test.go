package policy

import (
	"context"
	"errors"
	"image/color"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
	"fennec/cua/internal/view"
)

func step(s protocol.Step) protocol.Step { return s }

func pt(x, y float64) *protocol.Target {
	return &protocol.Target{Point: &protocol.Point{X: x, Y: y}}
}

func sel(s string) *protocol.Target { t := protocol.TargetFromString(s); return &t }

func str(s string) *string { return &s }

// counting answers yes or no and counts questions.
type counting struct {
	mu        sync.Mutex
	answer    bool
	questions []string
	err       error
	during    func()
}

func (c *counting) Confirm(ctx context.Context, q string) (bool, error) {
	c.mu.Lock()
	c.questions = append(c.questions, q)
	f := c.during
	c.mu.Unlock()
	if f != nil {
		f()
	}
	return c.answer, c.err
}

func TestConfirmCoordinate(t *testing.T) {
	gated := []protocol.Step{
		{Click: pt(1, 2)}, {Press: sel("e1")}, {Drag: pt(1, 1), To: pt(2, 2)},
		{Type: str("x"), Into: pt(3, 4)},
	}
	free := []protocol.Step{
		{Key: "cmd+s"}, {Type: str("x")}, {Type: str("x"), Into: sel(`textbox "Name"`)}, {Scroll: pt(1, 1)},
		{Menu: "File > Save"}, {Wait: &protocol.WaitRequest{Changed: true}}, {Expect: &protocol.Expect{WindowGone: "x"}},
	}
	for _, s := range gated {
		if !Gated(ConfirmPoint, s) {
			t.Errorf("%+v is not gated", s)
		}
	}
	for _, s := range free {
		if Gated(ConfirmPoint, s) {
			t.Errorf("%+v is gated", s)
		}
	}
	c := &counting{answer: false}
	g := &Gate{Mode: ConfirmPoint, Confirmer: c}
	perr := g.Ask(context.Background(), []protocol.Step{{Key: "tab"}, {Click: pt(1, 2)}, {Press: sel("e3")}}, "Notes")
	if perr == nil || perr.Code != protocol.ErrDeclined || !strings.Contains(perr.Message, "nothing was sent") {
		t.Fatalf("%+v", perr)
	}
	if len(c.questions) != 1 {
		t.Fatalf("a multi-step act asks once: %v", c.questions)
	}
	if perr := g.Ask(context.Background(), free, "Notes"); perr != nil || len(c.questions) != 1 {
		t.Fatalf("keys, plain typing and scroll pass: %+v", perr)
	}
	// No dialog can be shown: refused with a hint to turn confirm off.
	g = &Gate{Mode: ConfirmPoint, Confirmer: &counting{err: ErrNoDialog}}
	perr = g.Ask(context.Background(), gated[:1], "")
	if perr == nil || perr.Code != protocol.ErrUnavailable || !strings.Contains(perr.Hint, "turn confirm off") {
		t.Fatalf("%+v", perr)
	}
}

func TestConfirmEvery(t *testing.T) {
	for _, s := range []protocol.Step{{Key: "a"}, {Type: str("x")}, {Scroll: pt(1, 1)}, {Menu: "File > Save"}, {Focus: "w1"}, {Launch: "Notes"}} {
		if !Gated(ConfirmAll, s) {
			t.Errorf("%+v is not gated", s)
		}
	}
	if Gated(ConfirmAll, protocol.Step{Wait: &protocol.WaitRequest{Still: true}}) {
		t.Error("a wait is not an action")
	}
	c := &counting{answer: true}
	g := &Gate{Mode: ConfirmAll, Confirmer: c}
	if perr := g.Ask(context.Background(), []protocol.Step{{Key: "a"}}, ""); perr != nil || len(c.questions) != 1 {
		t.Fatalf("%+v %v", perr, c.questions)
	}
}

func TestConfirmModes(t *testing.T) {
	for in, want := range map[string]string{"": ConfirmOff, "off": ConfirmOff, "coordinate": ConfirmPoint, "point": ConfirmPoint, "every": ConfirmAll, "ALL": ConfirmAll} {
		got, err := NormalizeConfirm(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, err := NormalizeConfirm("sometimes"); err == nil {
		t.Error("an unknown mode was accepted")
	}
	if NeedsConfirm(ConfirmOff, []protocol.Step{{Click: pt(1, 1)}}) {
		t.Error("confirm off asked")
	}
	if !NeedsConfirm(ConfirmPoint, []protocol.Step{{Key: "a"}, {Click: pt(1, 1)}}) || NeedsConfirm(ConfirmPoint, []protocol.Step{{Key: "a"}}) {
		t.Error("coordinate mode")
	}
}

func TestConfirmAllowedIsOwnInput(t *testing.T) {
	now := time.Unix(1000, 0)
	act := &Activity{Now: func() time.Time { return now }, Idle: func() time.Duration { return 100 * time.Millisecond }}
	if act.Check() == nil {
		t.Fatal("a click 0.1 s ago is the user's")
	}
	g := &Gate{Mode: ConfirmPoint, Confirmer: AllowAll{}, Activity: act}
	if perr := g.Ask(context.Background(), []protocol.Step{{Click: pt(1, 1)}}, ""); perr != nil {
		t.Fatal(perr)
	}
	if perr := act.Check(); perr != nil {
		t.Fatalf("the Allow click counted as the user's input: %+v", perr)
	}
}

func TestConfirmQuestionText(t *testing.T) {
	q := Question("fennec-cua", []protocol.Step{{Click: pt(10, 20), Mods: []string{"cmd"}}}, "Notes")
	if q != `Allow fennec-cua to click at (10, 20) holding cmd in "Notes"?` {
		t.Fatalf("%q", q)
	}
	long := strings.Repeat("a", 300)
	q = Question("fennec-cua", []protocol.Step{{Type: &long}}, "")
	if !strings.Contains(q, `"`+strings.Repeat("a", TextClip-1)+`…"`) {
		t.Fatalf("typed text is clipped to %d: %q", TextClip, q)
	}
	q = Question("fennec-cua", []protocol.Step{{Type: str("hunter2"), Secret: true}}, "")
	if strings.Contains(q, "hunter2") {
		t.Fatal("the secret shows")
	}
	var many []protocol.Step
	for i := 0; i < 12; i++ {
		many = append(many, protocol.Step{Click: pt(float64(i), 1)})
	}
	q = Question("fennec-cua", many, "Notes")
	if !strings.HasPrefix(q, "Allow fennec-cua to run 12 steps: 1. click at (0, 1)") || !strings.Contains(q, "more)") {
		t.Fatalf("%q", q)
	}
	for _, part := range strings.Split(q, "; ") {
		if n := len([]rune(strings.TrimPrefix(part, "Allow fennec-cua to run 12 steps: "))); n > StepClip+4 {
			t.Errorf("step %q is longer than %d", part, StepClip)
		}
	}
}

func TestConfirmQuestionEscapes(t *testing.T) {
	q := Question("fennec-cua", []protocol.Step{{Type: str("ok\nAllow everything ‮evil\x07")}}, "")
	for _, bad := range []string{"\n", " ", "‮", "\x07"} {
		if strings.Contains(q, bad) {
			t.Fatalf("%q survives in %q", bad, q)
		}
	}
	for _, want := range []string{`\u{A}`, `\u{2028}`, `\u{202E}`, `\u{7}`} {
		if !strings.Contains(q, want) {
			t.Fatalf("%s missing from %q", want, q)
		}
	}
}

func TestConfirmText(t *testing.T) {
	if Verb("click") != "click" || Verb("do\nit") != `"do\u{A}it"` {
		t.Fatalf("%q %q", Verb("click"), Verb("do\nit"))
	}
	q := Question("fennec-cua", []protocol.Step{{Key: "cmd+q"}}, "Notes")
	if q != `Allow fennec-cua to key in "Notes": "cmd+q"?` {
		t.Fatalf("%q", q)
	}
	q = Question("fennec-cua", []protocol.Step{{Type: str("line\nnext")}}, "TextEdit")
	if q != `Allow fennec-cua to type in "TextEdit": "line\u{A}next"?` {
		t.Fatalf("%q", q)
	}
	q = Question("fennec-cua", []protocol.Step{{Drag: pt(1, 2), To: pt(3, 4), Mods: []string{"alt"}}}, "Board")
	if q != `Allow fennec-cua to drag at (1, 2) to (3, 4) holding alt in "Board"?` {
		t.Fatalf("%q", q)
	}
}

func TestConfirmAfterTurn(t *testing.T) {
	turn := &sched.Turn{Busy: 30 * time.Millisecond}
	held, err := turn.Acquire(context.Background(), "other")
	if err != nil {
		t.Fatal(err)
	}
	c := &counting{answer: true}
	g := &Gate{Mode: ConfirmPoint, Confirmer: c}
	acquire := func(ctx context.Context) (func(), error) { return turn.Acquire(ctx, "me") }
	steps := []protocol.Step{{Click: pt(1, 1)}}
	if _, err := g.Turn(context.Background(), acquire, steps, ""); !errors.Is(err, sched.ErrBusy) {
		t.Fatalf("%v", err)
	}
	if len(c.questions) != 0 {
		t.Fatal("asked before holding the turn")
	}
	held()
	var holder string
	c.during = func() { holder = turn.Holder() }
	release, err := g.Turn(context.Background(), acquire, steps, "")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if holder != "me" {
		t.Fatalf("the dialog opened while %q held the turn", holder)
	}
	// A no releases the turn.
	c.answer = false
	if _, err := g.Turn(context.Background(), acquire, steps, ""); err == nil || turn.Holder() != "" {
		t.Fatalf("%v, holder %q", err, turn.Holder())
	}
}

func TestCallsFailWhileDialogOpen(t *testing.T) {
	p, _ := New(Config{Confirm: "coordinate"}, nil)
	open, answer := make(chan struct{}), make(chan struct{})
	p.Gate.Confirmer = &counting{answer: true, during: func() { close(open); <-answer }}
	done := make(chan *protocol.Error)
	go func() { done <- p.Gate.Ask(context.Background(), []protocol.Step{{Click: pt(1, 1)}}, "") }()
	<-open
	for _, tool := range []string{protocol.ToolObserve, protocol.ToolAct, protocol.ToolWait} {
		perr := p.Admit(tool)
		if perr == nil || !strings.Contains(perr.Message, "confirm dialog is open") {
			t.Fatalf("%s: %+v", tool, perr)
		}
	}
	if p.Admit(protocol.ToolStatus) != nil {
		t.Fatal("status failed")
	}
	close(answer)
	if perr := <-done; perr != nil {
		t.Fatal(perr)
	}
	if perr := p.Admit(protocol.ToolAct); perr != nil {
		t.Fatalf("after the answer: %+v", perr)
	}
}

func TestRecordStartConfirm(t *testing.T) {
	c := &counting{answer: false}
	g := &Gate{Mode: ConfirmPoint, Confirmer: c}
	if perr := g.AskRecord(context.Background(), true); perr == nil || perr.Code != protocol.ErrDeclined {
		t.Fatalf("%+v", perr)
	}
	if perr := g.AskRecord(context.Background(), false); perr != nil || len(c.questions) != 1 {
		t.Fatalf("the app's own menu asked: %+v", perr)
	}
	g.Mode = ConfirmOff
	if g.AskRecord(context.Background(), true) != nil || len(c.questions) != 1 {
		t.Fatal("confirm off asked")
	}
}

func TestBadConfigFailsAllButStatus(t *testing.T) {
	for _, bad := range []string{`{"input":"sideways"}`, `{"confirm":"maybe"}`, `{"allowlist":"Notes"}`, `{"unknown":1}`, `not json`} {
		if _, err := Load([]byte(bad)); err == nil {
			t.Errorf("%s loaded", bad)
		}
	}
	c, err := Load([]byte(`{"input":"auto","allowlist":[" Notes ",""],"confirm":"every","locked_input":true}`))
	if err != nil || c.Input != protocol.InputAuto || c.Confirm != ConfirmAll || len(c.Allowlist) != 1 || c.Allowlist[0] != "Notes" {
		t.Fatalf("%+v %v", c, err)
	}
	_, err = Load([]byte(`{"input":"sideways"}`))
	p := Failed(err)
	for _, tool := range protocol.Tools {
		perr := p.Admit(tool)
		if tool == protocol.ToolStatus {
			if perr != nil {
				t.Fatalf("status: %+v", perr)
			}
			continue
		}
		if perr == nil || !strings.Contains(perr.Message, "sideways") {
			t.Fatalf("%s: %+v", tool, perr)
		}
	}
}

func TestDeliveryModeMismatchRefused(t *testing.T) {
	for _, tc := range []struct {
		configured, asked, want string
	}{
		{protocol.InputForeground, protocol.InputForeground, protocol.InputForeground},
		{protocol.InputBackground, protocol.InputBackground, protocol.InputBackground},
		{protocol.InputAuto, protocol.InputForeground, protocol.InputForeground},
		{protocol.InputAuto, "", protocol.InputAuto},
		{protocol.InputBackground, protocol.InputForeground, ""},
		{protocol.InputForeground, protocol.InputBackground, ""},
		{protocol.InputForeground, protocol.InputAuto, ""},
	} {
		got, perr := StepMode(tc.configured, tc.asked)
		if tc.want == "" {
			if perr == nil || !strings.Contains(perr.Message, "nothing was sent") {
				t.Errorf("%s asked %s: %q %+v", tc.configured, tc.asked, got, perr)
			}
			continue
		}
		if perr != nil || got != tc.want {
			t.Errorf("%s asked %s: %q %+v", tc.configured, tc.asked, got, perr)
		}
	}
	// The request narrows, then the step: a step cannot widen it again.
	if _, perr := StepMode(protocol.InputAuto, protocol.InputForeground, protocol.InputBackground); perr == nil {
		t.Error("a step widened the request's mode")
	}
}

func TestBackgroundNeedsTarget(t *testing.T) {
	for _, kind := range []string{"key", "type", "scroll", "drag"} {
		if perr := BackgroundTarget(kind, false); perr == nil || !strings.Contains(perr.Message, "nothing was sent and nothing was raised") {
			t.Errorf("%s: %+v", kind, perr)
		}
		if BackgroundTarget(kind, true) != nil {
			t.Errorf("%s with a window refused", kind)
		}
	}
	if BackgroundTarget("click", false) != nil {
		t.Error("a click finds its window at its point")
	}
	if !PointerRefused("click: Background delivery is not available for target window class 'X'") {
		t.Error("a refused pointer route")
	}
}

func TestAutoEscalatesPreSendRefusals(t *testing.T) {
	for _, tc := range []struct{ kind, reason, detail string }{
		{"key", ReasonNoWindow, ""},
		{"click", ReasonDesktop, ""},
		{"type", ReasonHidden, ""},
		{"scroll", ReasonOffSpace, ""},
		{"drag", ReasonPointer, ""},
		{"key", ReasonDriver, "press_key: window 7 is minimized or its application is hidden (or that state could not be proven)"},
		{"key", ReasonDriver, "press_key: window 7 is not among the process's current AXWindows (another Space)"},
	} {
		if !Escalates(tc.kind, tc.reason, tc.detail) {
			t.Errorf("%+v", tc)
		}
	}
}

func TestAutoKeepsPostSendErrors(t *testing.T) {
	for _, tc := range []struct{ kind, reason, detail string }{
		{"press", ReasonHidden, ""},
		{"key", ReasonTwins, ""},
		{"type", ReasonLocked, ""},
		{"key", ReasonAppNoWindow, ""},
		{"key", ReasonPolicy, ""},
		{"key", ReasonDriver, "press_key: window 7 cannot receive input: the screen is locked"},
		{"focus", ReasonNoWindow, ""},
	} {
		if Escalates(tc.kind, tc.reason, tc.detail) {
			t.Errorf("%+v", tc)
		}
	}
}

func refused(reason string) Attempt {
	return Attempt{Result: protocol.StepResult{Status: protocol.StatusRefused, Error: &protocol.Error{Message: "refused"}}, Reason: reason}
}

func TestAutoEscalatesOnceWhenNothingSent(t *testing.T) {
	fg := 0
	a := Auto("key", func() Attempt { return refused(ReasonHidden) }, func() Attempt {
		fg++
		return refused(ReasonHidden)
	})
	if fg != 1 || a.Result.Input != protocol.InputForeground || !strings.Contains(a.Result.Notes[0], "ran in the foreground") {
		t.Fatalf("%d %+v", fg, a)
	}
}

func TestAutoNoResendAfterPartialSend(t *testing.T) {
	fg := 0
	partial := Attempt{Result: protocol.StepResult{Status: protocol.StatusUnconfirmed, Error: &protocol.Error{Message: "line 2 of 3 failed"}}, Reason: ReasonDriver, Detail: "is minimized or its application is hidden"}
	a := Auto("type", func() Attempt { return partial }, func() Attempt { fg++; return Attempt{} })
	if fg != 0 || a.Result.Status != protocol.StatusUnconfirmed {
		t.Fatalf("resent: %d", fg)
	}
}

func TestAutoKeepsUnconfirmedInBackground(t *testing.T) {
	fg := 0
	bg := Attempt{Result: protocol.StepResult{Status: protocol.StatusUnconfirmed, Notes: []string{"the driver's note"}}}
	a := Auto("click", func() Attempt { return bg }, func() Attempt { fg++; return Attempt{} })
	if fg != 0 || a.Result.Notes[0] != "the driver's note" {
		t.Fatalf("%d %+v", fg, a)
	}
}

func TestLockScreenRefusalHint(t *testing.T) {
	p, _ := New(Config{}, nil)
	for _, mode := range []string{protocol.InputForeground, protocol.InputBackground, protocol.InputAuto} {
		perr := p.Locked("type", nil, mode, "TextEdit", true)
		if perr == nil || perr.Code != protocol.ErrScreenLocked || !strings.Contains(perr.Message, "routes input to the lock screen") || !strings.Contains(perr.Message, "password field") {
			t.Fatalf("%s: %+v", mode, perr)
		}
	}
}

func TestLockedInputKeysAndTypingOnly(t *testing.T) {
	p, _ := New(Config{LockedInput: true}, nil)
	if p.Locked("type", nil, protocol.InputBackground, "TextEdit", true) != nil || p.Locked("key", []string{"return"}, protocol.InputBackground, "TextEdit", true) != nil {
		t.Fatal("typing and single keys go through")
	}
	for _, kind := range []string{"click", "press", "scroll", "drag", "set", "menu", "focus"} {
		if p.Locked(kind, nil, protocol.InputBackground, "TextEdit", true) == nil {
			t.Errorf("%s went through a locked screen", kind)
		}
	}
	if p.Locked("type", nil, protocol.InputForeground, "TextEdit", true) == nil {
		t.Error("foreground typing went through a locked screen")
	}
	if perr := p.Locked("type", nil, protocol.InputBackground, "TextEdit", false); perr == nil || perr.Code != protocol.ErrUnavailable {
		t.Errorf("a driver without locked delivery: %+v", perr)
	}
	if !strings.Contains(LockedDelivered("TextEdit"), "key window of TextEdit as it was before the lock") || !strings.Contains(LockedDelivered("loginwindow"), "lock screen") {
		t.Error(LockedDelivered("TextEdit"))
	}
}

func TestLockedInputNeverTargetsLoginwindow(t *testing.T) {
	p, _ := New(Config{LockedInput: true}, nil)
	perr := p.Locked("type", nil, protocol.InputBackground, "loginwindow", true)
	if perr == nil || !strings.Contains(perr.Message, "no focused window") {
		t.Fatalf("%+v", perr)
	}
}

func TestLockedChordRefused(t *testing.T) {
	p, _ := New(Config{LockedInput: true}, nil)
	if perr := p.Locked("key", []string{"cmd", "q"}, protocol.InputBackground, "TextEdit", true); perr == nil || !strings.Contains(perr.Message, "chords") {
		t.Fatalf("%+v", perr)
	}
}

func activity(idle, sinceOwn time.Duration) *Activity {
	now := time.Unix(1000, 0)
	a := &Activity{Now: func() time.Time { return now }, Idle: func() time.Duration { return idle }}
	if sinceOwn >= 0 {
		a.ownEnd = now.Add(-sinceOwn)
	}
	return a
}

func TestUserBusyWindow(t *testing.T) {
	perr := activity(500*time.Millisecond, -1).Check()
	if perr == nil || !strings.Contains(perr.Message, "0.5 s ago") || !strings.Contains(perr.Message, "nothing was sent") || !strings.Contains(perr.Hint, "safe") {
		t.Fatalf("%+v", perr)
	}
	if activity(1200*time.Millisecond, -1).Check() != nil {
		t.Fatal("an idle user")
	}
	// Our own input ended 0.5 s ago and the event came 0.3 s ago: within 0.6 s after it.
	if perr := activity(300*time.Millisecond, 500*time.Millisecond).Check(); perr != nil {
		t.Fatalf("own input counted: %+v", perr)
	}
	// Our input ended 1.0 s ago and an event came 0.2 s ago: the user's.
	if activity(200*time.Millisecond, time.Second).Check() == nil {
		t.Fatal("a user event after the grace was ignored")
	}
}

func TestYieldWindow(t *testing.T) {
	a := activity(0, -1)
	idle := time.Duration(0)
	a.Idle = func() time.Duration { return idle }
	if a.Check() == nil {
		t.Fatal("the user is at the mouse")
	}
	idle = UserWindow
	if a.Check() != nil {
		t.Fatal("the user stopped a second ago")
	}
	idle = 50 * time.Millisecond
	a.Own()
	if a.Check() != nil {
		t.Fatal("the event right after our own input is ours")
	}
	var nilAct *Activity
	if nilAct.Check() != nil {
		t.Fatal("no activity guard")
	}
}

func TestVisionRefusedWithAllowlist(t *testing.T) {
	p, _ := New(Config{Allowlist: []string{"Notes"}}, nil)
	for _, input := range []bool{false, true} {
		if perr := p.Vision(input); perr == nil || perr.Code != protocol.ErrNotAllowed {
			t.Fatalf("%v: %+v", input, perr)
		}
	}
	p, _ = New(Config{}, nil)
	p.Activity.Idle = func() time.Duration { return time.Hour }
	if p.Vision(false) != nil || p.Vision(true) != nil {
		t.Fatal("no allowlist")
	}
}

func TestVisionClickNeedsForeground(t *testing.T) {
	p, _ := New(Config{Input: protocol.InputBackground}, nil)
	if perr := p.Vision(true); perr == nil || !strings.Contains(perr.Message, "needs the foreground") {
		t.Fatalf("%+v", perr)
	}
	if p.Vision(false) != nil {
		t.Fatal("a vision observe is no input")
	}
}

func TestBackgroundRefusesVisionActs(t *testing.T) {
	p, _ := New(Config{Input: protocol.InputBackground}, nil)
	p.Activity.Idle = func() time.Duration { panic("the activity check ran before the refusal") }
	perr := p.Vision(true)
	if perr == nil || !strings.Contains(perr.Hint, "semantic targets") {
		t.Fatalf("%+v", perr)
	}
}

func TestVisionInputUserActivityGuard(t *testing.T) {
	p, _ := New(Config{}, nil)
	p.Activity.Idle = func() time.Duration { return 300 * time.Millisecond }
	if perr := p.Vision(true); perr == nil || !strings.Contains(perr.Message, "0.3 s ago") {
		t.Fatalf("%+v", perr)
	}
	p.Activity.Own()
	if perr := p.Vision(true); perr != nil {
		t.Fatalf("our own injected input: %+v", perr)
	}
}

func wins() []view.RawWindow {
	return []view.RawWindow{
		{Key: "cg:2", App: "Finder", Title: "Downloads", Bounds: &[4]int{25, 25, 50, 50}},
		{Key: "cg:1", App: "TextEdit", Title: "Notes", Bounds: &[4]int{0, 0, 50, 50}},
	}
}

func TestAllowlistHidesWindows(t *testing.T) {
	a := NewAllowlist([]string{"textedit"})
	got := a.Filter(wins())
	if len(got) != 1 || got[0].App != "TextEdit" {
		t.Fatalf("%+v", got)
	}
	if !NewAllowlist(nil).Admits("Anything") || len(NewAllowlist(nil).Filter(wins())) != 2 {
		t.Fatal("an empty allowlist admits all")
	}
	if a.Window(&wins()[0]) == nil || a.Window(nil) == nil || a.Window(&wins()[1]) != nil {
		t.Fatal("window gate")
	}
}

func TestAllowlistGatesEveryPoint(t *testing.T) {
	a := NewAllowlist([]string{"TextEdit"})
	ws := wins()
	if a.Points(ws, [2]int{10, 10}) != nil {
		t.Fatal("a point over Notes")
	}
	if a.Points(ws, [2]int{10, 10}, [2]int{40, 40}) == nil {
		t.Fatal("a drag ending over Finder")
	}
	if perr := a.Points(ws, [2]int{500, 500}); perr == nil || !strings.Contains(perr.Message, "over no app window") {
		t.Fatalf("over no window: %+v", perr)
	}
}

func TestCaptureBlackout(t *testing.T) {
	img := SolidImage(100, 100, color.White)
	NewAllowlist([]string{"TextEdit"}).Blackout(Capture{Image: img, Scale: 1}, wins())
	white := func(x, y int) bool { r, _, _, _ := img.At(x, y).RGBA(); return r != 0 }
	if !white(10, 10) || white(30, 30) || white(80, 80) {
		t.Fatalf("kept %v, covered %v, outside %v", white(10, 10), white(30, 30), white(80, 80))
	}
	// A capture of part of the screen at scale 2.
	img = SolidImage(50, 50, color.White)
	NewAllowlist([]string{"TextEdit"}).Blackout(Capture{Image: img, Origin: [2]float64{0, 0}, Scale: 2}, wins())
	if !white(5, 5) || white(20, 20) {
		t.Fatal("scaled masking")
	}
}

func TestCaptureMaskStableWindows(t *testing.T) {
	// Without an allowlist: one capture, no window list.
	lists, caps := 0, 0
	list := func(context.Context) ([]view.RawWindow, error) { lists++; return wins(), nil }
	capture := func(context.Context) (Capture, error) {
		caps++
		return Capture{Image: SolidImage(10, 10, color.White), Scale: 1}, nil
	}
	if _, err := NewAllowlist(nil).MaskedCapture(context.Background(), list, capture); err != nil || lists != 0 || caps != 1 {
		t.Fatalf("%v %d %d", err, lists, caps)
	}
	// Windows that never hold still: no capture is shown.
	moving := func(context.Context) ([]view.RawWindow, error) {
		lists++
		ws := wins()
		ws[0].Bounds = &[4]int{lists, 0, 10, 10}
		return ws, nil
	}
	caps = 0
	if _, err := NewAllowlist([]string{"TextEdit"}).MaskedCapture(context.Background(), moving, capture); !errors.Is(err, ErrUnstable) || caps != CaptureTries {
		t.Fatalf("%v after %d captures", err, caps)
	}
}

func TestMaskStableWindowList(t *testing.T) {
	// A window opens during the first capture; the second holds still.
	n := 0
	list := func(context.Context) ([]view.RawWindow, error) {
		n++
		ws := wins()
		if n >= 2 {
			ws = append([]view.RawWindow{{Key: "cg:3", App: "Mail", Bounds: &[4]int{0, 0, 10, 10}}}, ws...)
		}
		return ws, nil
	}
	caps := 0
	capture := func(context.Context) (Capture, error) {
		caps++
		return Capture{Image: SolidImage(100, 100, color.White), Scale: 1}, nil
	}
	c, err := NewAllowlist([]string{"TextEdit"}).MaskedCapture(context.Background(), list, capture)
	if err != nil || caps != 2 {
		t.Fatalf("%v %d", err, caps)
	}
	if r, _, _, _ := c.Image.At(5, 5).RGBA(); r != 0 {
		t.Fatal("Mail's window, opened during the capture, is not masked")
	}
}

func TestAllowlistRefusesLaunchFlags(t *testing.T) {
	a := NewAllowlist([]string{"Google Chrome"})
	if a.Launch("Google Chrome", []string{"https://example.com", "notes.txt"}) != nil {
		t.Fatal("URLs and plain arguments pass")
	}
	if perr := a.Launch("Google Chrome", []string{"--renderer-cmd-prefix=/bin/sh"}); perr == nil || perr.Code != protocol.ErrNotAllowed {
		t.Fatalf("%+v", perr)
	}
	if a.Launch("Terminal", nil) == nil {
		t.Fatal("an app outside the allowlist launched")
	}
	if NewAllowlist(nil).Launch("Terminal", []string{"-n"}) != nil {
		t.Fatal("without an allowlist options pass")
	}
}

func TestLaunchOptionArgsRefused(t *testing.T) {
	a := NewAllowlist([]string{"Safari"})
	for _, arg := range []string{"-a", "--args", " -x"} {
		if a.Launch("Safari", []string{arg}) == nil {
			t.Errorf("%q passed", arg)
		}
	}
}
