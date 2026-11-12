package vision

import (
	"context"
	"testing"
	"time"
)

// timedThumbs answers thumbnails by the fake clock's time since start.
type timedThumbs struct {
	clock *fakeClock
	start time.Time
	at    func(d time.Duration) *Frame
	caps  []*Capture
}

func (s *timedThumbs) Thumb(context.Context) (*Frame, *Capture, error) {
	f := s.at(s.clock.since(s.start))
	c := CaptureOf(f)
	s.caps = append(s.caps, c)
	return f, c, nil
}

func tone(v uint8) *Frame { return solid(160, 100, v, v, v) }

func TestFramesDifferPastTolerance(t *testing.T) {
	a := solid(4, 4, 100, 100, 100)
	b := a.Clone()
	b.Set(3, 3, 148, 100, 100)
	if Differs(a, b) {
		t.Fatal("a difference of 48 counted")
	}
	b.Set(3, 3, 149, 100, 100)
	if !Differs(a, b) {
		t.Fatal("a difference of 49 did not count")
	}
	b.Set(3, 3, 100, 100, 51)
	if !Differs(a, b) {
		t.Fatal("a difference of 49 downwards did not count")
	}
	if !Differs(a, solid(4, 3, 100, 100, 100)) {
		t.Fatal("a size change did not count")
	}
}

func TestThumbnailChangeDetection(t *testing.T) {
	screen := ui(1600, 1000)
	before := Thumb(screen)
	if before.W != ThumbEdge || before.H != 100 {
		t.Fatalf("thumbnail %dx%d", before.W, before.H)
	}
	opened := screen.Clone()
	opened.Fill(Rect{400, 300, 600, 400}, 30, 30, 30) // a dialog opens
	if !Differs(before, Thumb(opened)) {
		t.Fatal("a dialog opening was not seen")
	}
	caret := screen.Clone()
	caret.Fill(Rect{800, 700, 1, 14}, 0, 0, 0) // a one-pixel caret
	if Differs(before, Thumb(caret)) {
		t.Fatal("a caret blink counted as a change")
	}
	if Differs(before, Thumb(screen.Clone())) {
		t.Fatal("the same screen differs")
	}
}

func TestSettledActionReturnsSettledCapture(t *testing.T) {
	clock := newClock()
	src := &timedThumbs{clock: clock, start: clock.Now(), at: func(d time.Duration) *Frame {
		switch {
		case d < 100*time.Millisecond:
			return tone(10)
		case d < 130*time.Millisecond:
			return tone(100) // shown for less than the hold
		}
		return tone(200)
	}}
	got := Settle(context.Background(), clock, tone(10), src, SettleOptions{})
	if !got.Still || !got.Changed || got.Capture == nil {
		t.Fatalf("%+v", got)
	}
	f, _ := got.Capture.Pixels()
	if r, _, _ := f.RGB(0, 0); r != 200 {
		t.Fatalf("the settled capture shows %d, not the screen it held still on", r)
	}
	if got.Elapsed > 400*time.Millisecond {
		t.Fatalf("a settled screen waited %v", got.Elapsed)
	}
	// The plan returns that capture as its screenshot instead of a new one.
	fc := &fakeCapturer{}
	svc := NewService(fc, nil)
	svc.Sessions.Keep("s", Shot{Kind: KindScreen, Scale: 1, Screen: [2]float64{1000, 1000}}, false)
	host := &fakeHost{svc: svc, session: "s", settled: got.Capture}
	res := RunPlan(context.Background(), svc.Sessions, "s", host, []PixelStep{{Action: "click", X: fp(5), Y: fp(5)}}, PlanOptions{})
	if res.Failed || res.Shot == nil || host.captured != 0 || fc.count("desktop") != 0 {
		t.Fatalf("plan %+v captured %d", res, host.captured)
	}
	if r, _, _ := res.Shot.Frame.RGB(0, 0); r != 200 {
		t.Fatal("the plan's screenshot is not the settled capture")
	}
}

func TestSettleRevertEndsEarly(t *testing.T) {
	clock := newClock()
	src := &timedThumbs{clock: clock, start: clock.Now(), at: func(d time.Duration) *Frame {
		if d >= 100*time.Millisecond && d < 300*time.Millisecond {
			return tone(150) // the page starts loading, then reverts
		}
		return tone(10)
	}}
	got := Settle(context.Background(), clock, tone(10), src, SettleOptions{Page: true})
	if got.Still || got.Changed {
		t.Fatalf("%+v", got)
	}
	if got.Elapsed < 690*time.Millisecond || got.Elapsed > 760*time.Millisecond {
		t.Fatalf("a reverted page change waited %v, want about 700 ms (first change plus 600 ms), not 3 s", got.Elapsed)
	}
	// A page change that stays keeps the settle open up to 3 s while it moves.
	clock = newClock()
	src = &timedThumbs{clock: clock, start: clock.Now(), at: func(d time.Duration) *Frame {
		return tone(uint8(60 + d/(80*time.Millisecond)%4*50))
	}}
	if got = Settle(context.Background(), clock, tone(10), src, SettleOptions{Page: true}); got.Elapsed < 2900*time.Millisecond || got.Elapsed > 3050*time.Millisecond {
		t.Fatalf("a moving page settled after %v", got.Elapsed)
	}
}

func TestDialogNoChangeSettle(t *testing.T) {
	if w := SettleWaitFor("click", "", "", true); w != SettleWait {
		t.Fatalf("a dialog click waits %v", w)
	}
	if w := SettleWaitFor("key", "return", "", false); w != ConfirmWait {
		t.Fatalf("Return waits %v", w)
	}
	if w := SettleWaitFor("type", "", "hello\n", false); w != ConfirmWait {
		t.Fatalf("a typed line waits %v", w)
	}
	clock := newClock()
	src := &timedThumbs{clock: clock, start: clock.Now(), at: func(time.Duration) *Frame { return tone(10) }}
	got := Settle(context.Background(), clock, tone(10), src, SettleOptions{Wait: SettleWaitFor("click", "", "", true)})
	if got.Changed || got.Elapsed != SettleWait {
		t.Fatalf("a click in a dialog that changes nothing waited %v", got.Elapsed)
	}
}

func TestProbeSourceUsesItsOwnSession(t *testing.T) {
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) { return CaptureOf(ui(1600, 1000)), nil }}
	thumb, c, err := ProbeSource{Capturer: fc, Session: "probe-1"}.Thumb(context.Background())
	if err != nil || thumb.W != ThumbEdge || c == nil {
		t.Fatal(err)
	}
	if call := fc.all()[0]; call.Session != "probe-1" || call.Edge != 0 {
		t.Fatalf("%+v", call)
	}
}
