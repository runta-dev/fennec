package actions

import (
	"context"
	"time"

	"fennec/cua/internal/view"
)

// MaxWindowWait caps a wait for the window list to change.
const MaxWindowWait = 5 * time.Second

// WindowPoll is how often the window list is read while waiting.
const WindowPoll = 100 * time.Millisecond

// WindowWait waits for the window list to change.
type WindowWait struct {
	// List reads the window list.
	List func(context.Context) ([]view.RawWindow, error)
	// Filter keeps only the windows the policy admits; both the baseline
	// and every fresh list pass it before comparing.
	Filter func([]view.RawWindow) []view.RawWindow
	Now    func() time.Time
	Sleep  func(context.Context, time.Duration) error
}

// Wait answers changed with the new list as soon as it differs from the
// baseline, or unchanged with the last list once timeout (capped at
// MaxWindowWait) has passed in wall time, list reads included. A list read
// that ends past the deadline ends the wait; a zero timeout reads once.
func (w WindowWait) Wait(ctx context.Context, baseline []view.RawWindow, timeout time.Duration) (bool, []view.RawWindow, error) {
	now := w.Now
	if now == nil {
		now = time.Now
	}
	sleep := w.Sleep
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
	filter := w.Filter
	if filter == nil {
		filter = func(ws []view.RawWindow) []view.RawWindow { return ws }
	}
	if timeout > MaxWindowWait {
		timeout = MaxWindowWait
	}
	if timeout < 0 {
		timeout = 0
	}
	base := filter(baseline)
	deadline := now().Add(timeout)
	for {
		ws, err := w.List(ctx)
		if err != nil {
			return false, nil, err
		}
		ws = filter(ws)
		if !sameList(base, ws) {
			return true, ws, nil
		}
		left := deadline.Sub(now())
		if left <= 0 {
			return false, ws, nil
		}
		if err := sleep(ctx, min(WindowPoll, left)); err != nil {
			return false, ws, err
		}
	}
}

func sameList(a, b []view.RawWindow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].Title != b[i].Title || a[i].Active != b[i].Active {
			return false
		}
	}
	return true
}
