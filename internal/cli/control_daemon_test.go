package cli

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServiceReplacementWaitsForRetiringRegistration(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitServiceRemoval(ctx, func() bool { calls++; return calls < 3 }); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("accepted stale registration", calls)
	}
}
func TestServiceReplacementStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitServiceRemoval(ctx, func() bool { return true }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
