package terminalstream

import (
	"context"
	"slices"
	"sync"
	"time"
)

const (
	echoQuiet   = 15 * time.Millisecond
	echoTimeout = 250 * time.Millisecond
	echoPoll    = 5 * time.Millisecond
	// Input cadence: a keystroke frame waits at most this long, and one
	// backlog is paced for at most cadenceBudget before writing at full speed.
	cadenceMax    = 50 * time.Millisecond
	cadenceBudget = time.Second
)

// echoAcker tells the client when the agent has reacted to its input, so a
// prediction that is still not on screen can be told apart from a busy agent.
type echoAcker struct {
	mu        sync.Mutex
	pending   bool
	seq       uint64
	writtenAt time.Time
	outputAt  time.Time
	output    bool
	wake      chan struct{}
}

func newEchoAcker() *echoAcker { return &echoAcker{wake: make(chan struct{}, 1)} }

// input records that input through seq has been written to the PTY.
func (a *echoAcker) input(seq uint64) {
	if a == nil || seq == 0 {
		return
	}
	a.mu.Lock()
	if seq > a.seq || !a.pending {
		a.seq = max(a.seq, seq)
	}
	a.pending, a.writtenAt, a.output = true, time.Now(), false
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// observe records agent output emitted to the client.
func (a *echoAcker) observe() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.pending {
		a.output, a.outputAt = true, time.Now()
	}
	a.mu.Unlock()
}

// due returns the acknowledgement to send now, if any.
func (a *echoAcker) due(now time.Time) (Frame, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.pending {
		return Frame{}, false
	}
	if a.output && now.Sub(a.outputAt) >= echoQuiet {
		a.pending = false
		return Frame{Type: "echo_ack", Seq: a.seq}, true
	}
	if now.Sub(a.writtenAt) >= echoTimeout {
		a.pending = false
		code := 0
		if !a.output {
			code = 1
		}
		return Frame{Type: "echo_ack", Seq: a.seq, Code: code}, true
	}
	return Frame{}, false
}

func (a *echoAcker) run(ctx context.Context, id string, emit func(Frame) error) {
	ticker := time.NewTicker(echoPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			frame, ok := a.due(time.Now())
			if !ok {
				a.mu.Lock()
				waiting := a.pending
				a.mu.Unlock()
				if waiting {
					continue
				}
				break
			}
			frame.ID = id
			if emit(frame) != nil {
				return
			}
			break
		}
	}
}

// cadence paces bunched keystroke frames.
type cadence struct {
	last  time.Time
	spent time.Duration
}

// wait returns how long to sleep before writing a frame typed delay after the
// previous one.
func (c *cadence) wait(now time.Time, delay time.Duration) time.Duration {
	if c.last.IsZero() || delay <= 0 {
		return 0
	}
	since := now.Sub(c.last)
	if since >= cadenceMax {
		c.spent = 0
		return 0
	}
	want := min(delay, cadenceMax) - since
	if want <= 0 || c.spent+want > cadenceBudget {
		return 0
	}
	c.spent += want
	return want
}

func (c *cadence) wrote(now time.Time) { c.last = now }

func hasFeature(features []string, name string) bool { return slices.Contains(features, name) }
