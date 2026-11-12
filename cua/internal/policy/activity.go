package policy

import (
	"fmt"
	"sync"
	"time"

	"fennec/cua/internal/protocol"
)

// Yielding to the user.
const (
	// UserWindow is how recent a user event must be to refuse foreground input.
	UserWindow = time.Second
	// OwnGrace is how long after fennec-cua's own foreground input an event
	// still counts as its own: macOS reports posted input that late.
	OwnGrace = 600 * time.Millisecond
)

// Activity refuses foreground input while the user is using the mouse or
// keyboard. Background input is not checked and is never own input.
type Activity struct {
	// Idle reports the time since the last input event of any source;
	// IdleTime by default.
	Idle func() time.Duration
	// Now is the clock; time.Now by default.
	Now func() time.Time

	mu     sync.Mutex
	ownEnd time.Time
}

func (a *Activity) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Check refuses with nothing sent when the user used the mouse or keyboard
// within UserWindow, unless that event came within OwnGrace after
// fennec-cua's own last foreground input ended.
func (a *Activity) Check() *protocol.Error {
	if a == nil {
		return nil
	}
	idle := IdleTime
	if a.Idle != nil {
		idle = a.Idle
	}
	since := idle()
	if since < 0 || since >= UserWindow {
		return nil
	}
	now := a.now()
	last := now.Add(-since)
	a.mu.Lock()
	own := a.ownEnd
	a.mu.Unlock()
	if !own.IsZero() && !last.After(own.Add(OwnGrace)) {
		return nil
	}
	return &protocol.Error{
		Code:    protocol.ErrBusy,
		Message: fmt.Sprintf("the user used the mouse or keyboard %.1f s ago, so fennec-cua yields; nothing was sent", since.Seconds()),
		Hint:    "retrying after the user stops is safe",
	}
}

// Own records that fennec-cua's own foreground input (or the user's answer
// to its confirm dialog) just ended.
func (a *Activity) Own() {
	if a == nil {
		return
	}
	t := a.now()
	a.mu.Lock()
	if t.After(a.ownEnd) {
		a.ownEnd = t
	}
	a.mu.Unlock()
}
