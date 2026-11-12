package sched

import (
	"context"
	"errors"
	"sync"
)

// ErrStopped is every call's answer while the user has stopped fennec-cua.
var ErrStopped = errors.New("stopped by the user; resume from Fennec's menu or with `fennec-cua resume`")

// Stopper is the user's kill switch. A stop cancels every call in flight and
// refuses new ones until resume.
type Stopper struct {
	mu      sync.Mutex
	stopped bool
	gen     uint64 // counts stops
	calls   map[uint64]context.CancelCauseFunc
	next    uint64
	onState func(stopped bool)
}

// OnState registers a callback for stop and resume.
func (s *Stopper) OnState(f func(bool)) {
	s.mu.Lock()
	s.onState = f
	s.mu.Unlock()
}

// Stopped reports the switch.
func (s *Stopper) Stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

// Stop cancels every guarded call and refuses new ones.
func (s *Stopper) Stop() {
	s.mu.Lock()
	s.stopped = true
	s.gen++
	calls := s.calls
	s.calls = nil
	f := s.onState
	s.mu.Unlock()
	for _, cancel := range calls {
		cancel(ErrStopped)
	}
	if f != nil {
		f(true)
	}
}

// Resume lets calls through again.
func (s *Stopper) Resume() {
	s.mu.Lock()
	s.stopped = false
	f := s.onState
	s.mu.Unlock()
	if f != nil {
		f(false)
	}
}

// Call is a guarded call.
type Call struct {
	s   *Stopper
	id  uint64
	gen uint64
}

// Guard starts a call: it fails with ErrStopped while stopped, and otherwise
// returns a context a stop cancels (with ErrStopped as its cause) and a done
// function that must be called when the call ends.
func (s *Stopper) Guard(ctx context.Context) (context.Context, *Call, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, nil, nil, ErrStopped
	}
	cctx, cancel := context.WithCancelCause(ctx)
	if s.calls == nil {
		s.calls = map[uint64]context.CancelCauseFunc{}
	}
	s.next++
	id := s.next
	s.calls[id] = cancel
	c := &Call{s: s, id: id, gen: s.gen}
	done := func() {
		s.mu.Lock()
		delete(s.calls, id)
		s.mu.Unlock()
		cancel(nil)
	}
	return cctx, c, done, nil
}

// Valid reports whether no stop came since the call began, even if a resume
// followed: a run queued when the user stopped must never start.
func (c *Call) Valid() error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.stopped || c.s.gen != c.gen {
		return ErrStopped
	}
	return nil
}

// Cause turns a context error into the error to report: ErrStopped when a
// stop ended the context.
func Cause(ctx context.Context) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return ctx.Err()
}
