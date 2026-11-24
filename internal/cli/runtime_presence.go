package cli

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"fennec/internal/cloudstate"
	"fennec/internal/runta"
)

// runtimePresenceInterval is how often a down resident channel asks the API
// whether its cloud computer still exists.
var runtimePresenceInterval = 5 * time.Second

// A single deletion answer never starts replacement. Deletion must be
// confirmed by at least runtimeDeletionConfirmations consecutive authoritative
// answers spanning runtimeDeletionConfirmSpan; any "exists" or "unknown"
// answer in between (or the channel coming back) starts over. This keeps a
// momentary API inconsistency from destroying a machine that is only offline.
var (
	runtimeDeletionConfirmations = 3
	runtimeDeletionConfirmSpan   = 30 * time.Second
)

var checkRuntimeGone = func(ctx context.Context, home, token, owner, id string) (bool, error) {
	return runta.NewClient(home, token).RuntimeGone(ctx, owner, id)
}

// runRuntimePresence notices a cloud computer deleted outside Fennec (for
// example in the Runta console). Nothing else recovers from that: the resident
// channel reconnects only to the configured runtime, preparation reuses services
// bound to it, and the controller keeps reporting "ready". The API is asked only
// while the channel is down, and only an authoritative deletion (a Runta
// not_found or deleted/deleting status, cross-checked against the owner's
// complete runtime listing; see runta.Client.RuntimeGone), confirmed
// repeatedly over time, starts replacement. A temporary
// disconnect (offline Mac, gateway or SSH failure, paused or stopped machine,
// API timeout/5xx/auth error, bare proxy 404) never does. A deletion made
// through Fennec is recorded first and keeps the cloud paused until the user
// creates again, so it is left alone here.
func (s *daemonServices) runRuntimePresence(ctx context.Context, epoch uint64) {
	ticker := time.NewTicker(runtimePresenceInterval)
	needed, span := runtimeDeletionConfirmations, runtimeDeletionConfirmSpan
	defer ticker.Stop()
	var replacing atomic.Bool
	// Consecutive authoritative deletion answers for suspect; reset by any
	// other answer, a recovered channel, or a different runtime.
	var (
		suspect     string
		confirmed   int
		firstGoneAt time.Time
	)
	reset := func() { suspect, confirmed, firstGoneAt = "", 0, time.Time{} }
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if s.network != nil && !s.network.Online() {
			// The Mac is offline: nothing observed now says anything about the
			// machine, and evidence gathered before the outage is stale.
			reset()
			continue
		}
		lifecycle, principal, current, ok := s.account.Session()
		if !ok || current != epoch {
			return
		}
		// Deletion is an authoritative Runta API answer; a self-hosted computer
		// is never presumed deleted.
		token, isRunta := principal.RuntaToken()
		if !isRunta {
			return
		}
		s.mu.Lock()
		channel, runtimeID, owner, replace := s.channel, s.config.RuntimeID, s.user, s.onRuntimeDeleted
		s.mu.Unlock()
		if replacing.Load() || channel == nil || runtimeID == "" || owner == "" || replace == nil || channel.peekAlive() != nil {
			reset()
			continue
		}
		check, cancel := context.WithTimeout(ctx, 10*time.Second)
		gone, err := checkRuntimeGone(check, s.home, token, owner, runtimeID)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil || !gone {
			// Exists (running, paused, stopped, ...) or unknown (network, 5xx,
			// auth, inconsistent answers): a temporary condition, never a deletion.
			reset()
			continue
		}
		if suspect != runtimeID {
			reset()
			suspect, firstGoneAt = runtimeID, time.Now()
		}
		confirmed++
		if confirmed < needed || time.Since(firstGoneAt) < span {
			continue
		}
		reset()
		store := cloudstate.NewScoped(s.home, runta.ActiveEnvironment(s.home).Key())
		if store.Check(owner) != nil || store.RecordReplacedRuntime(owner, runtimeID) != nil {
			continue
		}
		log.Printf("cloud computer %s confirmed deleted outside Fennec; preparing a replacement", runtimeID)
		// Teardown waits for every service worker, this one included, so the
		// replacement runs outside the worker group. Once teardown succeeds ctx
		// is canceled and this loop ends; after a failure it checks again.
		replacing.Store(true)
		go func() {
			defer replacing.Store(false)
			work, cancel := context.WithTimeout(lifecycle, 2*time.Minute)
			defer cancel()
			if err := replace(work, epoch, owner, runtimeID); err != nil {
				log.Printf("cloud computer replacement failed: %v", err)
			}
		}()
	}
}
