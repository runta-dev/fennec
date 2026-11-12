package ax

import (
	"context"
	"time"
)

// Window-change polling.
const (
	// PollTick is how often the cheap fingerprint is checked.
	PollTick = 10 * time.Millisecond
	// PollList is the least time between full window-list reads while the
	// fingerprint holds still.
	PollList = 100 * time.Millisecond
)

// WindowPoll waits for a window change cheaply: it checks a fingerprint of
// the on-screen windows every PollTick and reads the full window list at
// once when the fingerprint changes; otherwise only every PollList or the
// last list read's duration, whichever is longer. A backend without a
// fingerprint (ok false) reads the list each time the last read's duration
// has passed.
type WindowPoll struct {
	Fingerprint func() (uint64, bool)
	// List reads the full window list and reports whether the wait is over.
	List func(ctx context.Context) (bool, error)
	// Now and Sleep default to the real clock.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// Run polls until List reports the wait is over, ctx ends or limit passes;
// it reports whether List ended it. A list read is always made at the end
// of the limit.
func (p WindowPoll) Run(ctx context.Context, limit time.Duration) (bool, error) {
	now := p.Now
	if now == nil {
		now = time.Now
	}
	sleep := p.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	start := now()
	hint, hasHint := p.Fingerprint()
	checked := now()
	var last time.Duration
	for {
		left := limit - now().Sub(start)
		tick := PollTick
		if left < tick {
			tick = max(left, 0)
		}
		if err := sleep(ctx, tick); err != nil {
			return false, err
		}
		cur, ok := p.Fingerprint()
		over := now().Sub(start) >= limit
		moved := ok && (!hasHint || cur != hint)
		every := last
		if ok {
			every = max(last, PollList)
		}
		due := now().Sub(checked) >= every
		if !over && !moved && !due {
			continue
		}
		hint, hasHint = cur, ok
		at := now()
		done, err := p.List(ctx)
		if err != nil {
			return false, err
		}
		last = now().Sub(at)
		checked = now()
		if done {
			return true, nil
		}
		if over || now().Sub(start) >= limit {
			return false, nil
		}
	}
}
