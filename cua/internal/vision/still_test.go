package vision

import (
	"context"
	"testing"
	"time"
)

// litFrame is a 32x16 frame (4x2 blocks) whose listed blocks have a tone.
func litFrame(w, h int, lit map[int]uint8) *Frame {
	f := NewFrame(w, h)
	cols := (w + BlockSize - 1) / BlockSize
	for b, tone := range lit {
		f.Fill(Rect{b % cols * BlockSize, b / cols * BlockSize, BlockSize, BlockSize}, tone, tone, tone)
	}
	return f
}

func TestBlockClassification(t *testing.T) {
	cases := []struct {
		seq  []uint64
		kind string
	}{
		{[]uint64{1}, Quiet},
		{[]uint64{1, 1, 2, 2}, Quiet},
		{[]uint64{1, 2, 1}, Toggling},
		{[]uint64{1, 2, 1, 2, 1}, Toggling},
		{[]uint64{1, 2, 3}, Churning},
		{[]uint64{1, 2, 1, 3}, Churning},
	}
	for _, c := range cases {
		if k, _ := Classify(c.seq); k != c.kind {
			t.Errorf("%v is %s, want %s", c.seq, k, c.kind)
		}
	}
	if !counts([]uint64{1, 2}, 5) || counts([]uint64{1, 2, 3}, 5) {
		t.Error("a quiet block must count and a churning one must not")
	}
	if counts([]uint64{1, 2, 1}, 2) || !counts([]uint64{1, 2, 1}, 3) {
		t.Error("a toggling block counts only with a third content")
	}
}

func TestStillWatchCounts(t *testing.T) {
	video := func(tone uint8) *Blocks { return BlocksOf(litFrame(32, 16, map[int]uint8{0: tone})) }
	var w Watch
	if !w.Counts(video(10), video(60)) || !w.Counts(video(60), video(110)) {
		t.Fatal("the first two changes must count")
	}
	if w.Counts(video(110), video(160)) {
		t.Fatal("a third content churns and stops counting")
	}
	blink := func(on bool) *Blocks {
		tone := uint8(0)
		if on {
			tone = 200
		}
		return BlocksOf(litFrame(32, 16, map[int]uint8{1: tone}))
	}
	var caret Watch
	if !caret.Counts(blink(false), blink(true)) || !caret.Counts(blink(true), blink(false)) {
		t.Fatal("the first blinks count")
	}
	if caret.Counts(blink(false), blink(true)) {
		t.Fatal("a caret back at a shown content must not count")
	}
	if !caret.Counts(blink(true), BlocksOf(litFrame(32, 16, map[int]uint8{1: 90}))) {
		t.Fatal("a toggling block with new contents counts")
	}
	if caret.Volatile() != 1 {
		t.Fatalf("volatile %d, want 1", caret.Volatile())
	}
	var faint Watch
	if faint.Counts(BlocksOf(litFrame(32, 16, map[int]uint8{2: 100})), BlocksOf(litFrame(32, 16, map[int]uint8{2: 124}))) {
		t.Fatal("a change of 24 in a channel must not count")
	}
	if !faint.Counts(BlocksOf(litFrame(32, 16, map[int]uint8{2: 100})), BlocksOf(litFrame(32, 16, map[int]uint8{2: 125}))) {
		t.Fatal("a change of 25 in a channel counts")
	}
	var resized Watch
	if !resized.Counts(BlocksOf(litFrame(32, 16, nil)), BlocksOf(litFrame(16, 16, nil))) {
		t.Fatal("a change of capture size counts")
	}
}

// script answers frames by call number.
type script struct {
	n     int
	frame func(n int) *Frame
}

func (s *script) Frame(context.Context) (*Frame, error) {
	s.n++
	return s.frame(s.n), nil
}

func TestStillnessHoldAndTimeout(t *testing.T) {
	ctx := context.Background()
	quiet := &script{frame: func(int) *Frame { return litFrame(320, 200, nil) }}
	clock := newClock()
	res, err := Still(ctx, clock, quiet, StillOptions{})
	if err != nil || !res.Still || res.Changed || res.Ms < 40 || res.Ms > 60 {
		t.Fatalf("a quiet screen: %+v %v", res, err)
	}
	// One change, then quiet: still once the hold passed after it.
	once := &script{frame: func(n int) *Frame {
		if n >= 3 {
			return litFrame(320, 200, map[int]uint8{5: 200})
		}
		return litFrame(320, 200, nil)
	}}
	if res, _ = Still(ctx, newClock(), once, StillOptions{Hold: 100 * time.Millisecond}); !res.Still || !res.Changed || res.Ms < 120 {
		t.Fatalf("one change: %+v", res)
	}
	// A screen whose new contents keep coming never holds.
	moving := &script{}
	moving.frame = func(n int) *Frame {
		f := NewFrame(320, 200)
		f.Fill(Rect{(n % 40) * 8, (n / 40 % 25) * 8, 8, 8}, 255, 255, 255)
		return f
	}
	if res, _ = Still(ctx, newClock(), moving, StillOptions{}); res.Still || !res.Changed || res.Ms < 600 || res.Ms > 620 {
		t.Fatalf("timeout: %+v", res)
	}
	if res, _ = Still(ctx, newClock(), moving, StillOptions{Timeout: 9 * time.Second}); res.Ms > 5010 {
		t.Fatalf("the timeout was not capped at 5 s: %+v", res)
	}
}

func TestStillnessIgnoresVolatileBlocks(t *testing.T) {
	// A spinner cycles through contents in one block from the start.
	spinner := &script{frame: func(n int) *Frame {
		return litFrame(320, 200, map[int]uint8{7: uint8(30 + 40*(n%5))})
	}}
	res, err := Still(context.Background(), newClock(), spinner, StillOptions{Timeout: 2 * time.Second})
	if err != nil || !res.Still || res.Volatile != 1 {
		t.Fatalf("a churning region held the answer back: %+v %v", res, err)
	}
	if res.Ms > 200 {
		t.Fatalf("took %d ms", res.Ms)
	}
}

func TestStillnessVolatileBlocks(t *testing.T) {
	// A caret blinks between two contents: once it toggled back it stops
	// counting, and the screen is still.
	caret := &script{frame: func(n int) *Frame {
		tone := uint8(0)
		if n%2 == 0 {
			tone = 220
		}
		return litFrame(320, 200, map[int]uint8{3: tone})
	}}
	res, _ := Still(context.Background(), newClock(), caret, StillOptions{Timeout: 2 * time.Second})
	if !res.Still || res.Volatile != 1 {
		t.Fatalf("a blinking caret: %+v", res)
	}
	// A caret that shows a third content counts again.
	third := &script{frame: func(n int) *Frame {
		tone := uint8(0)
		switch {
		case n == 6:
			tone = 120
		case n%2 == 0:
			tone = 220
		}
		return litFrame(320, 200, map[int]uint8{3: tone})
	}}
	clock := newClock()
	start := clock.Now()
	res, _ = Still(context.Background(), clock, third, StillOptions{Hold: 30 * time.Millisecond})
	if !res.Still || !res.Changed || clock.since(start) < 80*time.Millisecond {
		t.Fatalf("a new caret content did not count: %+v after %v", res, clock.since(start))
	}
}

func TestStillSourceCapturesThinFrames(t *testing.T) {
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) {
		return CaptureOf(solid(320, 200, 1, 1, 1)), nil
	}}
	src := StillSource{Capturer: fc, Session: "capture"}
	if _, err := src.Frame(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := fc.all()[0]; c.Edge != StillEdge || c.Session != "capture" {
		t.Fatalf("still capture %+v", c)
	}
}
