package sched

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTurnIsExclusiveReentrantAndBounded(t *testing.T) {
	turn := &Turn{Busy: 50 * time.Millisecond}
	ctx := context.Background()
	release, err := turn.Acquire(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := turn.Acquire(ctx, "run-1")
	if err != nil {
		t.Fatalf("holder could not re-enter: %v", err)
	}
	inner()
	if turn.Holder() != "run-1" {
		t.Fatalf("inner release freed the turn")
	}
	start := time.Now()
	if _, err := turn.Acquire(ctx, "agent-2"); !errors.Is(err, ErrBusy) {
		t.Fatalf("want busy, got %v", err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Error("busy before the bound")
	}
	release()
	release() // idempotent
	r2, err := turn.Acquire(ctx, "agent-2")
	if err != nil {
		t.Fatal(err)
	}
	r2()
}

func TestTurnHandsOverInOrderOfRelease(t *testing.T) {
	turn := &Turn{}
	ctx := context.Background()
	release, _ := turn.Acquire(ctx, "a")
	got := make(chan string, 1)
	go func() {
		r, err := turn.Acquire(ctx, "b")
		if err == nil {
			got <- turn.Holder()
			r()
		}
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case <-got:
		t.Fatal("b acquired while a held the turn")
	default:
	}
	release()
	select {
	case h := <-got:
		if h != "b" {
			t.Errorf("holder %q", h)
		}
	case <-time.After(time.Second):
		t.Fatal("b never acquired")
	}
}

func TestActingCallback(t *testing.T) {
	turn := &Turn{}
	var mu sync.Mutex
	var states []bool
	done := make(chan struct{}, 2)
	turn.OnActing(func(b bool) { mu.Lock(); states = append(states, b); mu.Unlock(); done <- struct{}{} })
	r, _ := turn.Acquire(context.Background(), "x")
	<-done
	r()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(states) != 2 || !states[0] || states[1] {
		t.Errorf("states %v", states)
	}
}

func TestStopCancelsAndRefusesUntilResume(t *testing.T) {
	var s Stopper
	ctx, call, done, err := s.Guard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	s.Stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel")
	}
	if !errors.Is(Cause(ctx), ErrStopped) {
		t.Errorf("cause %v", Cause(ctx))
	}
	if _, _, _, err := s.Guard(context.Background()); !errors.Is(err, ErrStopped) {
		t.Errorf("guard while stopped: %v", err)
	}
	s.Resume()
	if call.Valid() == nil {
		t.Error("a call from before the stop is valid after resume")
	}
	_, fresh, done2, err := s.Guard(context.Background())
	if err != nil || fresh.Valid() != nil {
		t.Fatalf("after resume: %v", err)
	}
	done2()
}
