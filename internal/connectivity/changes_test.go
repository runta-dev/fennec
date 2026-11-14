package connectivity

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestNetworkEventsWakeWhileStillOnlineAndCoalesceBursts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan struct{}, 100)
	wake := watchChanges(ctx, events, func() string { return "same-online-network" }, time.Hour, 20*time.Millisecond, 100*time.Millisecond)
	for i := 0; i < 100; i++ {
		events <- struct{}{}
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("routing change did not wake recovery")
	}
	select {
	case <-wake:
		t.Fatal("one burst produced repeated wakes")
	case <-time.After(140 * time.Millisecond):
	}
	events <- struct{}{}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("later change was lost")
	}
	cancel()
	select {
	case _, ok := <-wake:
		if ok {
			t.Fatal("unexpected queued event")
		}
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop")
	}
}

func TestNetworkFingerprintFallbackSurvivesEventSourceFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan struct{})
	close(events)
	var generation atomic.Int64
	wake := watchChanges(ctx, events, func() string { return fmt.Sprint(generation.Load()) }, 10*time.Millisecond, 10*time.Millisecond, 30*time.Millisecond)
	select {
	case <-wake:
		t.Fatal("initial state is not a change")
	case <-time.After(40 * time.Millisecond):
	}
	generation.Store(1)
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("interface/DNS fallback did not wake recovery")
	}
}

func TestMonitorBroadcastsPathChangesWithoutOfflineTransition(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan struct{}, 1)
	monitor := NewMonitor(func() bool { return true }, time.Hour)
	monitor.watchChanges = func(context.Context) <-chan struct{} { return events }
	monitor.Start(ctx)
	changed := monitor.Changes()
	events <- struct{}{}
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("shared daemon did not receive path change")
	}
	if !monitor.Online() {
		t.Fatal("path change was incorrectly treated as offline")
	}
}
