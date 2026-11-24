package controlplane

import (
	"context"
	"fennec/internal/account"
	"fennec/internal/connectivity"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadyComputerThatCannotBeReachedIsReportedUnreachable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "private"}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	epoch := manager.Snapshot().Epoch
	var down atomic.Bool
	var askedEpoch atomic.Uint64
	online := true
	c := &Controller{
		Account:      manager,
		Connectivity: connectivity.NewMonitor(func() bool { return online }, time.Hour),
		CloudUnreachable: func(e uint64) bool {
			askedEpoch.Store(e)
			return down.Load()
		},
	}
	c.runtime = RuntimeState{Epoch: epoch, RuntimeID: "owned", Stage: "ready"}
	if stage := c.runtimeSnapshot().Stage; stage != "ready" {
		t.Fatalf("reachable computer reported %q", stage)
	}
	down.Store(true)
	state := c.runtimeSnapshot()
	if state.Stage != "unreachable" || state.RuntimeID != "owned" || askedEpoch.Load() != epoch {
		t.Fatalf("unreachable computer reported %+v (asked epoch %d)", state, askedEpoch.Load())
	}
	if c.runtime.Stage != "ready" {
		t.Fatal("reporting changed the preparation state")
	}
	down.Store(false)
	if stage := c.runtimeSnapshot().Stage; stage != "ready" {
		t.Fatalf("reconnected computer reported %q", stage)
	}

	// Only a prepared computer can be unreachable; preparation keeps its stage.
	down.Store(true)
	c.runtime.Stage = "preparing"
	if stage := c.runtimeSnapshot().Stage; stage != "preparing" {
		t.Fatalf("preparation reported %q", stage)
	}
}
