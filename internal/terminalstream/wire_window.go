package terminalstream

import (
	"encoding/json"
	"fmt"
	"sync"
)

const CapabilityWireWindow = "wire_output_window_v1"

// outputWireWindow bounds a terminal stream's encoded output in flight. Credit
// returns once per round trip, so it caps a stream at about window/RTT: 4 KiB
// gave about 17 KB/s at 250 ms on the real link (docs/predictive-echo.md,
// "Scrolling the transcript"), a seventh of the host's 128 KB/s.
const outputWireWindow = 64 << 10

// Keep bulk bytes handed to SSH bounded when TCP slows after packet loss.
// A 16 KiB encoded window retains useful throughput at 180 ms RTT without
// allowing the previous 32 KiB of one socket's output ahead of a fresh pong.
const socketWireWindow = 16 << 10

// A peer opting in promises to return credits as it consumes each frame,
// rather than waiting for the full legacy 64 KiB window. Bound encoded bytes
// already handed to SSH, including JSON/base64 overhead. Compressed repaints
// retain the larger original-byte window. Budgets are per stream: a stalled
// terminal must not consume another terminal's allowance.
type wireWindow struct {
	mu            sync.Mutex
	changed       *sync.Cond
	closed        bool
	used, pending int
	limit         int
	frames        []wireCredit
}

type wireCredit struct{ original, encoded int }

func newWireWindow() *wireWindow {
	w := &wireWindow{limit: outputWireWindow}
	w.changed = sync.NewCond(&w.mu)
	return w
}

func (w *wireWindow) reserve(original, encoded int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for !w.closed && w.used+encoded > w.limit {
		w.changed.Wait()
	}
	if w.closed {
		return false
	}
	w.used += encoded
	w.pending += original
	w.frames = append(w.frames, wireCredit{original, encoded})
	return true
}

func (w *wireWindow) ack(count int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if count <= 0 || count > w.pending || w.closed {
		return false
	}
	w.pending -= count
	for count > 0 {
		frame := &w.frames[0]
		n := min(count, frame.original)
		frame.original -= n
		count -= n
		if frame.original == 0 {
			w.used -= frame.encoded
			w.frames = w.frames[1:]
		}
	}
	w.changed.Broadcast()
	return true
}

func (w *wireWindow) close() {
	w.mu.Lock()
	w.closed = true
	w.changed.Broadcast()
	w.mu.Unlock()
}

func (session *serverSession) emitOutput(frame Frame, original int, emit func(Frame) error) error {
	frame.interactive = session.images
	if session.wire != nil {
		encoded, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		if len(encoded)+1 > outputWireWindow {
			return fmt.Errorf("output frame exceeds wire window")
		}
		// Publish credit accounting before writing: the peer may acknowledge
		// immediately, even before emit returns.
		if !session.wire.reserve(original, len(encoded)+1) {
			return session.ctx.Err()
		}
	}
	return emit(frame)
}
