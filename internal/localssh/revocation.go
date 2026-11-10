package localssh

import (
	"context"
	"time"
)

// WatchPairing bounds revocation latency for existing operations, including
// long-lived transfers. It never cancels unrelated devices or remote runtimes.
func watchPairing(ctx context.Context, cancel context.CancelFunc, valid func() bool, interval time.Duration) {
	if !valid() {
		cancel()
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !valid() {
					cancel()
					return
				}
			}
		}
	}()
}
