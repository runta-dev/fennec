package cli

import (
	"context"
	"testing"
	"time"
)

func TestCloudShutdownWaitsForWorkersAndFencesIdentity(t *testing.T) {
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &daemonServices{ctx: lifecycle, cancel: cancel, user: "owner", epoch: 3, config: brokerConfiguration{RuntimeID: "original"}, brokerReady: true}
	ctx, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	if s.quiesceCloud(ctx, "other", "original", 3) == nil {
		t.Fatal("wrong account shutdown allowed")
	}
	if lifecycle.Err() != nil {
		t.Fatal("rejected shutdown canceled user work")
	}
	release := make(chan struct{})
	s.workers.Add(1)
	go func() { defer s.workers.Done(); <-release }()
	short, finish := context.WithTimeout(ctx, 10*time.Millisecond)
	defer finish()
	if s.quiesceCloud(short, "owner", "original", 3) == nil {
		t.Fatal("shutdown succeeded before workers drained")
	}
	if lifecycle.Err() == nil {
		t.Fatal("service cancellation not issued")
	}
	if s.initializeMu.TryLock() {
		s.initializeMu.Unlock()
		t.Fatal("new initialization admitted while workers remained")
	}
	close(release)
	if err := s.quiesceCloud(ctx, "owner", "original", 3); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx != nil || s.channel != nil || s.brokerReady || s.connectorReady {
		t.Fatal("closed services retained live state")
	}
	if s.user != "owner" || s.config.RuntimeID != "original" {
		t.Fatal("shutdown lost deletion recovery identity")
	}
}
