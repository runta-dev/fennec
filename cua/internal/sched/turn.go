// Package sched decides who may act on the desktop. One owner holds the desk
// turn at a time; a holder may re-enter (a plan's own steps pass while the
// plan holds the turn); everyone else waits up to a bound and then gets
// ErrBusy. Reads never take the turn.
package sched

import (
	"context"
	"errors"
	"sync"
	"time"
)

// DefaultBusy is how long a caller waits for the turn.
const DefaultBusy = 15 * time.Second

// ErrBusy means the turn stayed taken for the whole bound; nothing was sent.
var ErrBusy = errors.New("the desktop is busy with another agent's action; nothing was sent, try again shortly")

// Turn is the desk turn.
type Turn struct {
	// Busy bounds the wait; zero means DefaultBusy.
	Busy time.Duration

	mu      sync.Mutex
	owner   string
	depth   int
	changed chan struct{} // closed and replaced whenever the turn frees

	acting   int
	onActing func(bool)
}

// OnActing registers a callback for the acting state (any holder or not),
// such as the menu-bar indicator.
func (t *Turn) OnActing(f func(bool)) {
	t.mu.Lock()
	t.onActing = f
	t.mu.Unlock()
}

// Acquire takes the turn for owner and returns its release. It fails with
// ErrBusy after the bound, or with ctx's error when ctx ends first.
func (t *Turn) Acquire(ctx context.Context, owner string) (release func(), err error) {
	bound := t.Busy
	if bound == 0 {
		bound = DefaultBusy
	}
	timer := time.NewTimer(bound)
	defer timer.Stop()
	for {
		t.mu.Lock()
		if t.changed == nil {
			t.changed = make(chan struct{})
		}
		if t.depth == 0 || t.owner == owner {
			t.owner = owner
			t.depth++
			t.setActing(+1)
			t.mu.Unlock()
			var once sync.Once
			return func() { once.Do(t.release) }, nil
		}
		wait := t.changed
		t.mu.Unlock()
		select {
		case <-wait:
		case <-timer.C:
			return nil, ErrBusy
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (t *Turn) release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.depth--
	t.setActing(-1)
	if t.depth == 0 {
		t.owner = ""
		close(t.changed)
		t.changed = make(chan struct{})
	}
}

// setActing must run with mu held.
func (t *Turn) setActing(delta int) {
	before := t.acting > 0
	t.acting += delta
	if after := t.acting > 0; after != before && t.onActing != nil {
		go t.onActing(after)
	}
}

// Holder reports who holds the turn, if anyone.
func (t *Turn) Holder() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.depth == 0 {
		return ""
	}
	return t.owner
}
