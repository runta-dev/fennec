package localssh

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestActiveOperationCancelledWhenPairingRevoked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var valid atomic.Bool
	valid.Store(true)
	watchPairing(ctx, cancel, valid.Load, time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatal("valid device cancelled")
	default:
	}
	valid.Store(false)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("revocation not propagated")
	}
}
func TestAlreadyRevokedOperationCancelledImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchPairing(ctx, cancel, func() bool { return false }, time.Hour)
	if ctx.Err() == nil {
		t.Fatal("revoked operation alive")
	}
}
