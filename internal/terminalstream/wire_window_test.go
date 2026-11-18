package terminalstream

import (
	"bytes"
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestWireWindowBoundsEncodedOutputAndKeepsCompressedRepaint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &serverSession{ctx: ctx, wire: newWireWindow()}
	var sent int
	emit := func(f Frame) error {
		data, err := json.Marshal(f)
		sent += len(data) + 1
		return err
	}
	// A 32 KiB repaint fits without a round trip when its wire representation
	// fits. Shrinking the original-byte window to 4 KiB would deadlock here.
	finished := make(chan error, 1)
	go func() {
		for range 16 {
			frame := compressTerminalData(Frame{Type: "data", ID: "paint", Data: bytes.Repeat([]byte(" "), OutputChunk)})
			if err := s.emitOutput(frame, OutputChunk, emit); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		s.wire.close()
		<-finished
		t.Fatal("compressed repaint blocked before filling encoded budget")
	}
	if sent > outputWireWindow || s.wire.pending != 16*OutputChunk {
		t.Fatalf("sent=%d pending=%d", sent, s.wire.pending)
	}
	if !s.wire.ack(16*OutputChunk) || s.wire.used != 0 {
		t.Fatal("coalesced credits did not release repaint")
	}
}

func TestWireWindowPartialCreditsAndPeerIsolation(t *testing.T) {
	frame := outputWireWindow * 3 / 4 // two frames do not fit
	w := newWireWindow()
	if !w.reserve(2000, frame) {
		t.Fatal("initial reservation")
	}
	if w.ack(2001) || w.ack(0) || w.ack(-1) {
		t.Fatal("accepted invalid credit")
	}
	var entered atomic.Bool
	done := make(chan bool, 1)
	go func() { entered.Store(true); done <- w.reserve(2000, frame) }()
	for !entered.Load() {
		time.Sleep(time.Millisecond)
	}
	if !w.ack(1000) {
		t.Fatal("partial credit refused")
	}
	select {
	case <-done:
		t.Fatal("released frame before consumption")
	case <-time.After(20 * time.Millisecond):
	}
	peer := newWireWindow()
	if !peer.reserve(2000, frame) {
		t.Fatal("stalled peer blocked independent stream")
	}
	if !w.ack(1000) {
		t.Fatal("remaining credit refused")
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("reservation failed")
		}
	case <-time.After(time.Second):
		t.Fatal("credit did not release writer")
	}
	w.close()
}

func TestWireWindowCancellationReleasesWriter(t *testing.T) {
	w := newWireWindow()
	w.reserve(2000, outputWireWindow*3/4)
	done := make(chan bool, 1)
	go func() { done <- w.reserve(2000, outputWireWindow*3/4) }()
	w.close()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("reservation survived close")
		}
	case <-time.After(time.Second):
		t.Fatal("writer leaked")
	}
}

// fakeClockWindow returns a terminal window driven by a fake clock.
func fakeClockWindow() (*wireWindow, *time.Time) {
	now := time.Unix(1000, 0)
	w := newWireWindow()
	w.now = func() time.Time { return now }
	return w, &now
}

// sendAndCredit sends one 1 KiB frame and credits it after rtt.
func sendAndCredit(t *testing.T, w *wireWindow, now *time.Time, rtt time.Duration) {
	t.Helper()
	if !w.reserve(OutputChunk, 1000) {
		t.Fatal("reserve")
	}
	*now = now.Add(rtt)
	if !w.ack(OutputChunk) {
		t.Fatal("ack")
	}
}

// TestWireWindowFollowsRoundTrip: the window is about one and a half
// bandwidth-delay products of the stream's own round trip, within bounds.
func TestWireWindowFollowsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		rtt      time.Duration
		min, max int
	}{
		{20 * time.Millisecond, outputWireWindowMin, outputWireWindowMin}, // floor
		{80 * time.Millisecond, 15 << 10, 16 << 10},
		{220 * time.Millisecond, 42 << 10, 43 << 10},
		{250 * time.Millisecond, 47 << 10, 49 << 10},
		{400 * time.Millisecond, outputWireWindowMax, outputWireWindowMax},
	} {
		w, now := fakeClockWindow()
		if w.limit != outputWireWindow {
			t.Fatalf("initial window %d", w.limit)
		}
		sendAndCredit(t, w, now, tc.rtt)
		if w.limit < tc.min || w.limit > tc.max {
			t.Errorf("rtt %v: window %d, want %d..%d", tc.rtt, w.limit, tc.min, tc.max)
		}
	}
}

// TestWireWindowIgnoresQueueing: round trips of frames that waited behind
// the stream's own output do not grow the window, and a minimum older than
// the filter expires, so a lasting change of path is followed.
func TestWireWindowIgnoresQueueing(t *testing.T) {
	w, now := fakeClockWindow()
	reserve := func() {
		t.Helper()
		if !w.reserve(OutputChunk, 1000) {
			t.Fatal("reserve")
		}
	}
	credit := func(after time.Duration) {
		t.Helper()
		*now = now.Add(after)
		if !w.ack(OutputChunk) {
			t.Fatal("ack")
		}
	}
	// Sustained output: one frame always in flight, so every frame after
	// the first waits behind the previous one for 900 ms.
	reserve()
	reserve()
	credit(80 * time.Millisecond)
	small := w.limit
	if small != adaptiveWindow(80*time.Millisecond) {
		t.Fatalf("window %d after an 80 ms round trip", small)
	}
	for i := 0; i < 20; i++ {
		reserve()
		credit(900 * time.Millisecond)
	}
	credit(900 * time.Millisecond)
	if w.limit != small {
		t.Fatalf("window grew from %d to %d on queued samples", small, w.limit)
	}
	for i := 0; i < 15; i++ { // the path really became slower
		*now = now.Add(time.Second)
		sendAndCredit(t, w, now, 300*time.Millisecond)
	}
	if w.limit != adaptiveWindow(300*time.Millisecond) {
		t.Fatalf("window %d after the round trip rose to 300 ms", w.limit)
	}
}

// TestWireWindowSmallerThanFrame: one frame always goes when nothing is in
// flight, however small the window became.
func TestWireWindowSmallerThanFrame(t *testing.T) {
	w, now := fakeClockWindow()
	sendAndCredit(t, w, now, time.Millisecond)
	if w.limit != outputWireWindowMin {
		t.Fatalf("window %d", w.limit)
	}
	done := make(chan bool, 1)
	go func() { done <- w.reserve(OutputChunk, outputWireWindowMin+500) }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("reservation failed")
		}
	case <-time.After(time.Second):
		t.Fatal("frame larger than the window blocked an idle stream")
	}
}

// TestSocketWindowFollowsRoundTrip: sockets size their window for the egress
// ceiling, within the bounds every peer's raw credit window already accepts.
func TestSocketWindowFollowsRoundTrip(t *testing.T) {
	cases := []struct {
		rtt  time.Duration
		want int
	}{
		{time.Millisecond, socketWireWindowMin},
		{50 * time.Millisecond, socketWindow(50 * time.Millisecond)},
		{400 * time.Millisecond, socketWireWindowMax},
	}
	for _, tc := range cases {
		w, now := fakeClockWindow()
		w.limit, w.socket = socketWireWindow, true
		sendAndCredit(t, w, now, tc.rtt)
		if w.limit != tc.want {
			t.Errorf("rtt %v: socket window %d, want %d", tc.rtt, w.limit, tc.want)
		}
	}
	if got := socketWindow(200 * time.Millisecond); got <= socketWireWindow {
		t.Fatalf("socket window at 200 ms is %d, no larger than the old fixed 16 KiB", got)
	}
	if socketWireWindowMax*2/3 > Window {
		t.Fatalf("socket window %d exceeds the peer's raw credit window", socketWireWindowMax)
	}
}
