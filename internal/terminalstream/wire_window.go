package terminalstream

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"fennec/internal/traffic"
)

const CapabilityWireWindow = "wire_output_window_v1"

// A terminal stream's encoded output in flight is bounded by a window that
// follows the stream's own round trip. Credit returns once per round trip, so
// a window caps a stream at window/RTT: a fixed 4 KiB gave about 17 KB/s at
// 250 ms on the real link (docs/predictive-echo.md, "Scrolling the
// transcript"), while a window above the bandwidth-delay product only queues
// output ahead of interactive traffic. The window is therefore
// wireWindowGain x the paced output rate x the minimum round trip seen over
// rttFilterSeconds (the minimum, so queueing caused by the window itself does
// not grow it), between outputWireWindowMin and outputWireWindowMax. Until a
// round trip is measured it is outputWireWindow.
const (
	outputWireWindow    = 16 << 10
	outputWireWindowMin = 8 << 10
	outputWireWindowMax = 64 << 10
	wireWindowGain      = 1.5
	rttFilterSeconds    = 10
)

// outputRate is the paced terminal output rate, bytes per second.
var outputRate = float64(traffic.DefaultConfig().BytesPerSecond)

// adaptiveWindow is the window for a minimum round trip.
func adaptiveWindow(rtt time.Duration) int {
	w := int(wireWindowGain * outputRate * rtt.Seconds())
	return max(outputWireWindowMin, min(outputWireWindowMax, w))
}

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
	// adaptive: limit follows the measured round trip (terminal output);
	// otherwise it is fixed (sockets).
	adaptive bool
	now      func() time.Time
	rtt      rttFilter
}

type wireCredit struct {
	original, encoded int
	sent              time.Time
	// alone: nothing of this stream was in flight when it was sent, so its
	// round trip includes no queueing behind the stream's own output.
	alone bool
}

func newWireWindow() *wireWindow {
	w := &wireWindow{limit: outputWireWindow, adaptive: true, now: time.Now}
	w.changed = sync.NewCond(&w.mu)
	return w
}

func (w *wireWindow) reserve(original, encoded int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	// With nothing in flight one frame always goes, even when a small round
	// trip made the window smaller than the frame.
	for !w.closed && w.used > 0 && w.used+encoded > w.limit {
		w.changed.Wait()
	}
	if w.closed {
		return false
	}
	alone := w.used == 0
	w.used += encoded
	w.pending += original
	w.frames = append(w.frames, wireCredit{original, encoded, w.now(), alone})
	return true
}

// observe records a frame's round trip (sent to fully credited).
func (w *wireWindow) observe(rtt time.Duration) {
	now := w.now()
	w.rtt.add(now, rtt)
	if w.adaptive {
		w.limit = adaptiveWindow(w.rtt.min(now))
	}
}

// rttFilter keeps the minimum round trip of each of the last
// rttFilterSeconds seconds.
type rttFilter struct {
	slots [rttFilterSeconds]struct {
		second int64
		rtt    time.Duration
	}
}

func (f *rttFilter) add(now time.Time, rtt time.Duration) {
	second := now.Unix()
	slot := &f.slots[second%rttFilterSeconds]
	if slot.second != second || rtt < slot.rtt {
		slot.second, slot.rtt = second, rtt
	}
}

func (f *rttFilter) min(now time.Time) time.Duration {
	best := time.Duration(-1)
	for _, slot := range f.slots {
		if slot.second > now.Unix()-rttFilterSeconds && slot.rtt > 0 && (best < 0 || slot.rtt < best) {
			best = slot.rtt
		}
	}
	return best
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
			if frame.alone {
				w.observe(w.now().Sub(frame.sent))
			}
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
		if len(encoded)+1 > outputWireWindowMax {
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
