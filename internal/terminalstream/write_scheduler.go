package terminalstream

import (
	"context"
	"sync"
)

// writeScheduler serializes whole protocol frames. Each caller waits until its
// write finishes, so bulk producers cannot enqueue unbounded output. Control
// traffic can pass other streams, never earlier frames of its own stream.
// Eight urgent writes at most can pass a bulk writer before it gets a turn.
// This cannot preempt bytes already handed to SSH; producers also use small
// chunks. The connection owner must close IO when canceling its context.
type writeScheduler struct {
	mu        sync.Mutex
	queue     []*scheduledWrite
	running   bool
	urgentRun int
	slots     chan struct{}
	ctx       context.Context
}

type scheduledWrite struct {
	id     string
	urgent bool
	run    func() error
	done   chan error
}

func newWriteScheduler(ctx context.Context) *writeScheduler {
	return &writeScheduler{ctx: ctx, slots: make(chan struct{}, 64)}
}

func urgentFrames(frames []Frame) bool {
	if len(frames) != 1 {
		return false
	}
	switch frames[0].Type {
	case "ping", "pong", "input", "input_credit", "credit", "resize", "focus":
		return true
	}
	return false
}

func (s *writeScheduler) write(id string, urgent bool, run func() error) error {
	select {
	case s.slots <- struct{}{}:
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	request := &scheduledWrite{id: id, urgent: urgent, run: run, done: make(chan error, 1)}
	s.mu.Lock()
	s.queue = append(s.queue, request)
	if !s.running {
		s.running = true
		go s.drain()
	}
	s.mu.Unlock()
	// Do not return while a running write still references the caller's byte
	// buffer. Cancellation closes the underlying stream and releases drain.
	return <-request.done
}

func (s *writeScheduler) drain() {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.running = false
			s.mu.Unlock()
			return
		}
		index := 0
		if s.urgentRun < 8 {
			seen := map[string]bool{}
			for i, request := range s.queue {
				if request.urgent && !seen[request.id] {
					index = i
					break
				}
				seen[request.id] = true
			}
		}
		request := s.queue[index]
		s.queue = append(s.queue[:index], s.queue[index+1:]...)
		if request.urgent {
			s.urgentRun++
		} else {
			s.urgentRun = 0
		}
		s.mu.Unlock()
		err := s.ctx.Err()
		if err == nil {
			err = request.run()
		}
		request.done <- err
		<-s.slots
	}
}
