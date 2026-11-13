package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fennec/cua/internal/engine"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
)

// axRig is an engine whose act runs its work on one serial accessibility
// queue while holding the desk turn, with a per-call limit, the way desk
// actions reach the accessibility worker.
type axRig struct {
	q     sched.Queue
	ran   atomic.Int32
	work  func() // what an act does on the queue
	limit time.Duration
}

func (r *axRig) act(ctx context.Context, call *engine.Call, _ json.RawMessage) (any, *protocol.Error) {
	release, err := call.Turn(ctx)
	if err != nil {
		return nil, engine.TurnError(err)
	}
	cctx, cancel := context.WithTimeout(ctx, r.limit)
	defer cancel()
	if err := r.q.Held(cctx, release, func() {
		r.ran.Add(1)
		if r.work != nil {
			r.work()
		}
	}); err != nil {
		return nil, engine.TurnError(err)
	}
	return map[string]any{"ok": true}, nil
}

// hang occupies the queue with a hung read (no turn) until the returned
// function is called.
func hang(q *sched.Queue) (unhang func()) {
	block := make(chan struct{})
	started := make(chan struct{})
	go func() { _, _ = q.Do(context.Background(), func() { close(started); <-block }) }()
	<-started
	var once sync.Once
	return func() { once.Do(func() { close(block) }) }
}

func axEngine(r *axRig, busy time.Duration) *served {
	eng := newEngine(map[string]engine.Tool{protocol.ToolAct: r.act})
	eng.Turn.Busy = busy
	return eng
}

func TestQueuedTimeoutNeverRuns(t *testing.T) {
	r := &axRig{limit: 100 * time.Millisecond}
	eng := axEngine(r, 2*time.Second)
	unhang := hang(&r.q)
	defer unhang()
	res := eng.Call(context.Background(), session("a"), "act", nil)
	if !res.IsError || !contains(res.Text, `"timeout"`) {
		t.Fatalf("queued act: %s", res.Text)
	}
	unhang()
	// Once the hung read ends, the queue runs later work but never the
	// call that gave up.
	if _, err := r.q.Do(context.Background(), func() {}); err != nil {
		t.Fatal(err)
	}
	if n := r.ran.Load(); n != 0 {
		t.Errorf("a timed-out call ran %d times late", n)
	}
	// Queue.Held lets go of the turn from its own goroutine once the
	// abandoned job is passed over: wait for it briefly.
	deadline := time.Now().Add(time.Second)
	for eng.Turn.Holder() != "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if eng.Turn.Holder() != "" {
		t.Errorf("turn still held by %q", eng.Turn.Holder())
	}
}

func TestQueuedTimeoutFreesTurn(t *testing.T) {
	r := &axRig{limit: 100 * time.Millisecond}
	eng := axEngine(r, 2*time.Second)
	unhang := hang(&r.q)
	defer unhang()
	start := time.Now()
	res := eng.Call(context.Background(), session("a"), "act", nil)
	if !res.IsError {
		t.Fatalf("queued act: %s", res.Text)
	}
	// The turn is let go at the timeout, while the read still hangs: the
	// next actor is not answered busy for nothing.
	waitFor(t, func() bool { return eng.Turn.Holder() == "" })
	if took := time.Since(start); took > time.Second {
		t.Errorf("turn freed after %v", took)
	}
	release, err := eng.Turn.Acquire(context.Background(), "b")
	if err != nil {
		t.Fatalf("next actor: %v", err)
	}
	release()
}

func TestRunningTimeoutHoldsTurn(t *testing.T) {
	block := make(chan struct{})
	r := &axRig{limit: 100 * time.Millisecond, work: func() { <-block }}
	eng := axEngine(r, 150*time.Millisecond)
	res := eng.Call(context.Background(), session("a"), "act", nil)
	if !res.IsError || !contains(res.Text, `"timeout"`) {
		t.Fatalf("running act: %s", res.Text)
	}
	// The action may still land: no one else acts until it really ends.
	if h := eng.Turn.Holder(); h != "a" {
		t.Fatalf("holder %q while the timed-out action runs", h)
	}
	if res := eng.Call(context.Background(), session("b"), "act", nil); !contains(res.Text, `"busy"`) {
		t.Errorf("another act during a timed-out running one: %s", res.Text)
	}
	close(block)
	waitFor(t, func() bool { return eng.Turn.Holder() == "" })
	r.work = nil
	if res := eng.Call(context.Background(), session("b"), "act", nil); res.IsError {
		t.Errorf("after it ended: %s", res.Text)
	}
}

func TestNoLateRun(t *testing.T) {
	r := &axRig{limit: 5 * time.Second}
	eng := axEngine(r, 5*time.Second)
	unhang := hang(&r.q)
	defer unhang()
	// A call queued when the user stops never runs, even after resume.
	got := make(chan string, 1)
	go func() { got <- eng.Call(context.Background(), session("a"), "act", nil).Text }()
	waitFor(t, func() bool { return eng.Turn.Holder() == "a" })
	eng.Stopper.Stop()
	if text := <-got; !contains(text, `"stopped"`) {
		t.Errorf("stopped while queued: %s", text)
	}
	eng.Stopper.Resume()
	// Several calls that time out together all give up; a call still
	// waiting when the read ends runs once.
	r.limit = 80 * time.Millisecond
	var wg sync.WaitGroup
	for _, who := range []string{"b", "c", "d"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = eng.Call(context.Background(), session(who), "act", nil)
		}()
	}
	wg.Wait()
	r.limit = 5 * time.Second
	done := make(chan string, 1)
	go func() { done <- eng.Call(context.Background(), session("e"), "act", nil).Text }()
	waitFor(t, func() bool { return eng.Turn.Holder() == "e" })
	unhang()
	if text := <-done; contains(text, "error") {
		t.Errorf("a waiting call: %s", text)
	}
	if n := r.ran.Load(); n != 1 {
		t.Errorf("%d calls ran; only the one still waiting should", n)
	}
}
