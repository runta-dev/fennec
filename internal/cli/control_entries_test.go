package cli

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRestoredSelectionReconcilesEntriesWithoutBrokerRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var integrations atomic.Int32
	s := &daemonServices{home: t.TempDir(), ctx: ctx, epoch: 1, selected: map[string]bool{"codex": true}, ready: map[string]bool{}, coverage: map[string]string{}, entryQueue: make(chan string, 8), entryPending: map[string]bool{}, brokerRunning: true, brokerReady: true, config: brokerConfiguration{RuntimeID: "owned"}}
	s.entryInstaller = func(context.Context, string) (string, error) {
		calls.Add(1)
		close(entered)
		<-release
		return "entry_and_new_shells", nil
	}
	// Selection must request integration, but this entry-worker test must not
	// spawn a background reconcile that discovers real host agent executables.
	s.integrationReconciler = func(home, executable, selection string) {
		if home != s.home || executable == "" || selection != "codex" {
			t.Errorf("unexpected integration target")
		}
		integrations.Add(1)
	}
	done := make(chan struct{})
	go func() { defer close(done); s.runEntryRepair(ctx, 1) }()
	s.mu.Lock()
	for i := 0; i < 8; i++ {
		s.reconcileSelectionLocked()
	}
	s.mu.Unlock()
	<-entered
	if calls.Load() != 1 {
		t.Fatal("duplicate entry repair")
	}
	if integrations.Load() != 8 {
		t.Fatal("selection did not request integration", integrations.Load())
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ready := s.coverage["codex"] == "entry_and_new_shells"
		sameBroker := s.brokerReady && s.brokerRunning
		s.mu.Unlock()
		if ready {
			if !sameBroker {
				t.Fatal("entry repair restarted broker")
			}
			cancel()
			<-done
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("restored entry never reconciled")
}
