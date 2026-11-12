package sched

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync"
	"sync/atomic"
)

// RunIDs hands out run owner ids that are unique across daemon restarts: a
// random nonce chosen once per daemon plus a counter, so a new daemon never
// mistakes an earlier daemon's run for one of its own.
type RunIDs struct {
	once  sync.Once
	nonce string
	n     atomic.Uint64
}

// Next returns a fresh run id, run-<nonce>-<n>.
func (r *RunIDs) Next() string {
	r.once.Do(func() {
		var b [8]byte
		_, _ = rand.Read(b[:])
		r.nonce = hex.EncodeToString(b[:])
	})
	return "run-" + r.nonce + "-" + strconv.FormatUint(r.n.Add(1), 10)
}

// Queue runs jobs one at a time in the order they were submitted, such as
// the calls of one accessibility worker. A job whose caller gave up while it
// was still queued never runs; a job that started runs to its end even when
// its caller gave up meanwhile.
type Queue struct {
	mu      sync.Mutex
	pending []*job
	busy    bool
}

const (
	queued = iota
	running
	abandoned
	finished
)

type job struct {
	fn    func()
	mu    sync.Mutex
	state int
	start chan struct{}
	done  chan struct{}
}

// Do runs fn after every job submitted before it. It returns nil once fn has
// run. When ctx ends first it returns ctx's error (the stop cause when there
// is one) and a channel that closes when fn is no longer going to run or
// has finished running: at once for a job still queued, which then never
// runs, and at fn's end for a job already running.
func (q *Queue) Do(ctx context.Context, fn func()) (<-chan struct{}, error) {
	j := &job{fn: fn, start: make(chan struct{}), done: make(chan struct{})}
	q.mu.Lock()
	q.pending = append(q.pending, j)
	if !q.busy {
		q.busy = true
		go q.work()
	}
	q.mu.Unlock()
	select {
	case <-j.done:
		return j.done, nil
	case <-ctx.Done():
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	switch j.state {
	case queued:
		j.state = abandoned
		gone := make(chan struct{})
		close(gone)
		return gone, Cause(ctx)
	case finished:
		return j.done, nil
	}
	return j.done, Cause(ctx)
}

// Held runs fn on the queue for a caller holding the desk turn and takes
// over release: the turn is let go as soon as fn is known never to run (a
// timeout or stop while queued) or once fn has finished, so an action that
// may still land keeps the turn until it really ends.
func (q *Queue) Held(ctx context.Context, release func(), fn func()) error {
	ended, err := q.Do(ctx, fn)
	go func() {
		<-ended
		release()
	}()
	return err
}

func (q *Queue) work() {
	for {
		q.mu.Lock()
		if len(q.pending) == 0 {
			q.busy = false
			q.mu.Unlock()
			return
		}
		j := q.pending[0]
		q.pending = q.pending[1:]
		q.mu.Unlock()
		j.mu.Lock()
		if j.state == abandoned {
			j.mu.Unlock()
			continue
		}
		j.state = running
		j.mu.Unlock()
		j.fn()
		j.mu.Lock()
		j.state = finished
		close(j.done)
		j.mu.Unlock()
	}
}
