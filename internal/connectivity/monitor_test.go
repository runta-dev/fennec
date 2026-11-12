package connectivity

import (
	"context"
	"errors"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestMonitorPausesWhileOfflineAndWakesOnReconnect(t *testing.T) {
	var online atomic.Bool
	m := NewMonitor(online.Load, 15*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m.Start(ctx)

	if m.Online() {
		t.Fatal("expected initial offline state")
	}

	woke := make(chan error, 1)
	go func() {
		woke <- m.WaitOnline(ctx)
	}()

	select {
	case err := <-woke:
		t.Fatalf("WaitOnline returned while still offline: %v", err)
	case <-time.After(45 * time.Millisecond):
	}

	changed := m.Changes()
	online.Store(true)

	select {
	case <-changed:
	case <-ctx.Done():
		t.Fatal("monitor did not broadcast online transition")
	}
	select {
	case err := <-woke:
		if err != nil {
			t.Fatalf("WaitOnline failed after reconnect: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("WaitOnline did not unblock on reconnect")
	}
}

func TestIsOfflineErrorClassification(t *testing.T) {
	if !IsOfflineError(errors.New("dial tcp: lookup api.runta.com: no such host"), false) {
		t.Fatal("expected any transport failure while offline to classify as offline")
	}
	if !IsOfflineError(syscall.ENETUNREACH, true) {
		t.Fatal("expected ENETUNREACH to classify as offline")
	}
	if !IsOfflineError(errors.New("ssh: connect to host ssh.runta.com port 22: No route to host"), true) {
		t.Fatal("expected no route to host text to classify as offline")
	}
	if IsOfflineError(errors.New("Runta HTTP 403"), true) {
		t.Fatal("HTTP 403 must not classify as offline")
	}
}
