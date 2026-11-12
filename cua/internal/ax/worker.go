package ax

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// TimeoutError is a worker call that did not answer within its limit.
type TimeoutError struct {
	Limit time.Duration
	// Ran is set when the call had started: its action may still land.
	Ran bool
}

func (e *TimeoutError) Error() string {
	if e.Ran {
		return fmt.Sprintf("accessibility did not answer within %d ms; the action may still take effect", e.Limit.Milliseconds())
	}
	return fmt.Sprintf("accessibility did not answer within %d ms (the call was queued behind a hung one and never ran); an earlier action may still take effect", e.Limit.Milliseconds())
}

// Worker runs accessibility calls in order on one goroutine that owns the
// backend state S. A call that does not answer within its limit fails, and
// the worker is replaced by a fresh one that opens its own state; any number
// of callers timing out behind the same hang cause exactly one replacement.
// A call that times out while still queued never runs. When the backend
// cannot open, each call fails with that reason and the next call tries to
// open again.
type Worker[S any] struct {
	// Open makes a backend state; it runs on the worker goroutine.
	Open func() (S, error)
	// Close, when set, drops a state; it runs on the worker goroutine once
	// the worker is replaced or closed.
	Close func(S)

	mu       sync.Mutex
	cur      *workerLoop[S]
	restarts atomic.Int64
}

// Job states.
const (
	jobQueued int32 = iota
	jobRunning
	jobAbandoned
)

type job[S any] struct {
	fn    func(S) error
	state atomic.Int32
	done  chan error
}

type workerLoop[S any] struct {
	w     *Worker[S]
	mu    sync.Mutex
	queue []*job[S]
	wake  chan struct{}
	dead  bool
}

// Restarts counts replacements after hangs.
func (w *Worker[S]) Restarts() int { return int(w.restarts.Load()) }

// Do runs fn on the worker within limit. It answers fn's error, a
// *TimeoutError, or ctx's error when ctx ends first (a queued call then
// never runs).
func (w *Worker[S]) Do(ctx context.Context, limit time.Duration, fn func(S) error) error {
	j := &job[S]{fn: fn, done: make(chan error, 1)}
	loop := w.enqueue(j)
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case err := <-j.done:
		return err
	case <-ctx.Done():
		if j.state.CompareAndSwap(jobQueued, jobAbandoned) {
			return ctx.Err()
		}
		select {
		case err := <-j.done:
			return err
		default:
			return ctx.Err()
		}
	case <-timer.C:
		ran := !j.state.CompareAndSwap(jobQueued, jobAbandoned)
		if ran {
			// It may have answered just now.
			select {
			case err := <-j.done:
				return err
			default:
			}
		}
		w.replace(loop)
		return &TimeoutError{Limit: limit, Ran: ran}
	}
}

// Shutdown stops the current worker; queued calls never run.
func (w *Worker[S]) Shutdown() {
	w.mu.Lock()
	loop := w.cur
	w.cur = nil
	w.mu.Unlock()
	if loop != nil {
		loop.kill()
	}
}

func (w *Worker[S]) enqueue(j *job[S]) *workerLoop[S] {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cur == nil {
		w.cur = w.start()
	}
	loop := w.cur
	loop.mu.Lock()
	loop.queue = append(loop.queue, j)
	loop.mu.Unlock()
	loop.signal()
	return loop
}

func (w *Worker[S]) start() *workerLoop[S] {
	l := &workerLoop[S]{w: w, wake: make(chan struct{}, 1)}
	go l.run()
	return l
}

// replace swaps a hung loop for a fresh one, moving its still-queued calls
// across. A loop already replaced is left alone, so callers stuck behind one
// hang replace it once.
func (w *Worker[S]) replace(old *workerLoop[S]) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cur != old {
		return
	}
	w.restarts.Add(1)
	fresh := w.start()
	w.cur = fresh
	old.mu.Lock()
	moved := old.queue
	old.queue = nil
	old.dead = true
	old.mu.Unlock()
	old.signal()
	if len(moved) > 0 {
		fresh.mu.Lock()
		fresh.queue = append(fresh.queue, moved...)
		fresh.mu.Unlock()
		fresh.signal()
	}
}

func (l *workerLoop[S]) signal() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *workerLoop[S]) kill() {
	l.mu.Lock()
	l.dead = true
	l.queue = nil
	l.mu.Unlock()
	l.signal()
}

func (l *workerLoop[S]) run() {
	var state S
	opened := false
	defer func() {
		if opened && l.w.Close != nil {
			l.w.Close(state)
		}
	}()
	for {
		l.mu.Lock()
		if l.dead {
			l.mu.Unlock()
			return
		}
		if len(l.queue) == 0 {
			l.mu.Unlock()
			<-l.wake
			continue
		}
		j := l.queue[0]
		l.queue = l.queue[1:]
		l.mu.Unlock()
		if !j.state.CompareAndSwap(jobQueued, jobRunning) {
			continue // its caller gave up; it never runs late
		}
		if !opened {
			s, err := l.w.Open()
			if err != nil {
				j.done <- err
				continue
			}
			state, opened = s, true
		}
		j.done <- j.fn(state)
	}
}
