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
	w := newWireWindow()
	if !w.reserve(2000, 3000) {
		t.Fatal("initial reservation")
	}
	if w.ack(2001) || w.ack(0) || w.ack(-1) {
		t.Fatal("accepted invalid credit")
	}
	var entered atomic.Bool
	done := make(chan bool, 1)
	go func() { entered.Store(true); done <- w.reserve(2000, 3000) }()
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
	if !peer.reserve(2000, 3000) {
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
	w.reserve(2000, 3000)
	done := make(chan bool, 1)
	go func() { done <- w.reserve(2000, 3000) }()
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
