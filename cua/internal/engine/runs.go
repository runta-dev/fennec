package engine

import (
	"context"
	"sync"
)

// holds are what a run took on the desk (driver sessions, web-tree
// switches); they are let go when the run ends, however it ends.
type holds struct {
	mu       sync.Mutex
	releases []func()
	done     bool
}

func (h *holds) add(release func()) {
	h.mu.Lock()
	if !h.done {
		h.releases = append(h.releases, release)
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	release() // the run already ended
}

// releaseAll lets go of every hold, the latest first.
func (h *holds) releaseAll() {
	h.mu.Lock()
	rs := h.releases
	h.releases, h.done = nil, true
	h.mu.Unlock()
	for i := len(rs) - 1; i >= 0; i-- {
		rs[i]()
	}
}

// Hold ties release to the run this call belongs to: it runs when the run
// ends, normally, by a stop, a timeout or a dropped connection. It reports
// false outside a run, where the caller keeps the hold's lifetime itself.
func (c *Call) Hold(release func()) bool {
	if c == nil || c.holds == nil {
		return false
	}
	c.holds.add(release)
	return true
}

// RunID is the owner id of the run this call belongs to, empty outside a
// run.
func (c *Call) RunID() string {
	if c == nil {
		return ""
	}
	return c.runID
}

// runCall is the call a run's own steps make: it takes the turn as the run,
// so the run's steps re-enter the turn it holds while every other call,
// even from the same session, waits.
func (e *Engine) runCall(call *Call) (*Call, func(ctx context.Context) (func(), error)) {
	id := e.runs.Next()
	acquire := call.acquire
	if acquire == nil {
		acquire = func(ctx context.Context, _ string) (func(), error) { return call.Turn(ctx) }
	}
	turn := func(ctx context.Context) (func(), error) { return acquire(ctx, id) }
	return &Call{Session: call.Session, Turn: turn, acquire: acquire, holds: &holds{}, runID: id}, turn
}

type peerKey struct{}

// WithPeer marks ctx as coming from a connection whose peer is (owner true)
// or is not the daemon's own user.
func WithPeer(ctx context.Context, owner bool) context.Context {
	return context.WithValue(ctx, peerKey{}, owner)
}

// PeerIsOwner reports whether ctx's connection runs as the daemon's own
// user; a context that names no peer (stdio, in-process) counts as the
// owner's.
func PeerIsOwner(ctx context.Context) bool {
	owner, ok := ctx.Value(peerKey{}).(bool)
	return !ok || owner
}
