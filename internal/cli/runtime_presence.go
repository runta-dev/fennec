package cli

import (
	"context"
	"sync/atomic"
	"time"

	"fennec/internal/cloudstate"
	"fennec/internal/runta"
)

// runtimePresenceInterval is how often a down resident channel asks the API
// whether its cloud computer still exists.
var runtimePresenceInterval = 5 * time.Second

var checkRuntimeGone = func(ctx context.Context, home, token, id string) (bool, error) {
	return runta.NewClient(home, token).RuntimeGone(ctx, id)
}

// runRuntimePresence notices a cloud computer deleted outside Fennec (for
// example in the Runta console). Nothing else recovers from that: the resident
// channel reconnects only to the configured runtime, preparation reuses services
// bound to it, and the controller keeps reporting "ready". The API is asked only
// while the channel is down, and only an authoritative answer (404, deleted or
// deleting) starts replacement; network or API errors never do. A deletion made
// through Fennec is recorded first and keeps the cloud paused until the user
// creates again, so it is left alone here.
func (s *daemonServices) runRuntimePresence(ctx context.Context, epoch uint64) {
	ticker := time.NewTicker(runtimePresenceInterval)
	defer ticker.Stop()
	var replacing atomic.Bool
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if s.network != nil && !s.network.Online() {
			continue
		}
		lifecycle, token, current, ok := s.account.Session()
		if !ok || current != epoch {
			return
		}
		s.mu.Lock()
		channel, runtimeID, owner, replace := s.channel, s.config.RuntimeID, s.user, s.onRuntimeDeleted
		s.mu.Unlock()
		if replacing.Load() || channel == nil || runtimeID == "" || owner == "" || replace == nil || channel.peekAlive() != nil {
			continue
		}
		check, cancel := context.WithTimeout(ctx, 10*time.Second)
		gone, err := checkRuntimeGone(check, s.home, token, runtimeID)
		cancel()
		if err != nil || !gone || ctx.Err() != nil {
			continue
		}
		store := cloudstate.NewScoped(s.home, runta.ActiveEnvironment(s.home).Key())
		if store.Check(owner) != nil || store.RecordReplacedRuntime(owner, runtimeID) != nil {
			continue
		}
		// Teardown waits for every service worker, this one included, so the
		// replacement runs outside the worker group. Once teardown succeeds ctx
		// is canceled and this loop ends; after a failure it checks again.
		replacing.Store(true)
		go func() {
			defer replacing.Store(false)
			work, cancel := context.WithTimeout(lifecycle, 2*time.Minute)
			defer cancel()
			_ = replace(work, epoch, owner, runtimeID)
		}()
	}
}
