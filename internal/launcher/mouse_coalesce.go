package launcher

import (
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Full-screen agents capture the mouse (all-motion tracking) and scroll their
// transcript themselves, so every wheel step and every pointer move is sent
// to the cloud, and each wheel step makes the agent redraw the whole screen
// (about 13 KB through Herdr). A trackpad gesture is dozens of steps, one
// per frame. mouseCoalescer sends a burst of mouse reports together once the
// previous burst has been answered, so an agent that renders once per frame
// (Ink, ratatui) redraws once per round trip instead of once per step:
// cmd/profile-scroll measured 51 KB instead of 436 KB for a 40-step gesture.
// Wheel steps, clicks and drags are never dropped; of pointer moves with no
// button held only the latest is kept. Any other input first sends what is
// held, so the order is unchanged.
type mouseCoalescer struct {
	send func([]byte, inputStamp) error
	// maxHold bounds how long reports wait for an acknowledgement; without
	// acknowledgements they are sent once per holdWindow.
	maxHold, holdWindow time.Duration
	echoAck             func() bool

	mu        sync.Mutex
	held      []mouseReport
	firstHeld time.Time // when the oldest held report arrived
	lastSent  time.Time // when the last burst was sent
	inFlight  uint64    // end sequence of the last mouse burst sent, 0: none
	ackSeq    uint64
	timer     *time.Timer
	closed    bool
	err       error // the first send error from a timer or acknowledgement flush
	// Stats for diagnostics.
	reports, sent, dropped, bursts int
}

type mouseReport struct {
	data  []byte
	end   uint64 // input sequence at the end of the report
	hover bool   // a pointer move with no button held
}

func newMouseCoalescer(send func([]byte, inputStamp) error, echoAck func() bool) *mouseCoalescer {
	return &mouseCoalescer{send: send, echoAck: echoAck, maxHold: 400 * time.Millisecond, holdWindow: 50 * time.Millisecond}
}

var sgrMouse = regexp.MustCompile(`^\x1b\[<(\d+);\d+;\d+[Mm]`)

// parseMouse splits data into SGR mouse reports; ok is false when data holds
// anything else.
func parseMouse(data []byte) (reports [][]byte, hover []bool, ok bool) {
	for len(data) > 0 {
		m := sgrMouse.FindSubmatchIndex(data)
		if m == nil {
			return nil, nil, false
		}
		button, _ := strconv.Atoi(string(data[m[2]:m[3]]))
		reports = append(reports, data[:m[1]])
		// Motion bit set and no button (3) after the modifier bits.
		hover = append(hover, button&32 != 0 && button&^(4|8|16|32) == 3)
		data = data[m[1]:]
	}
	return reports, hover, len(reports) > 0
}

// Send passes input on, holding packets of mouse reports while an earlier
// burst is unanswered.
func (c *mouseCoalescer) Send(data []byte, stamp inputStamp) error {
	reports, hover, mouse := parseMouse(data)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if !mouse {
		if err := c.flushLocked(); err != nil {
			return err
		}
		return c.send(data, stamp)
	}
	c.reports += len(reports)
	if len(c.held) == 0 {
		c.firstHeld = time.Now()
	}
	end := stamp.seq
	for i, r := range reports {
		end += uint64(len(r))
		if hover[i] {
			// A newer pointer position replaces a held one.
			for j := len(c.held) - 1; j >= 0; j-- {
				if c.held[j].hover {
					c.held = append(c.held[:j], c.held[j+1:]...)
					c.dropped++
					break
				}
			}
		}
		c.held = append(c.held, mouseReport{data: append([]byte(nil), r...), end: end, hover: hover[i]})
	}
	if c.waitingLocked() {
		c.armLocked()
		return nil
	}
	return c.flushLocked()
}

// waitingLocked: an earlier burst has not been answered yet.
func (c *mouseCoalescer) waitingLocked() bool {
	if c.inFlight == 0 {
		return false
	}
	if c.echoAck() {
		return c.ackSeq < c.inFlight && time.Since(c.firstHeld) < c.maxHold
	}
	return time.Since(c.lastSent) < c.holdWindow
}

func (c *mouseCoalescer) armLocked() {
	if c.timer != nil || c.closed {
		return
	}
	wait := c.holdWindow - time.Since(c.lastSent)
	if c.echoAck() {
		wait = c.maxHold - time.Since(c.firstHeld)
	}
	c.timer = time.AfterFunc(max(0, wait), func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.timer = nil
		if err := c.flushLocked(); err != nil && c.err == nil {
			c.err = err
		}
	})
}

// flushLocked sends the held reports as one packet with no typing gap (the
// cloud's input cadence would otherwise space them out again).
func (c *mouseCoalescer) flushLocked() error {
	if len(c.held) == 0 {
		return nil
	}
	var data []byte
	for _, r := range c.held {
		data = append(data, r.data...)
	}
	end := c.held[len(c.held)-1].end
	c.sent += len(c.held)
	c.bursts++
	c.held = nil
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.inFlight, c.lastSent = end, time.Now()
	// Dropped moves leave a gap in the sequence: the packet ends where its
	// last report ended, so acknowledgements still line up.
	return c.send(data, inputStamp{seq: end - uint64(len(data))})
}

// Close sends what is held and stops the timer.
func (c *mouseCoalescer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return c.flushLocked()
}

// Ack records the cloud's acknowledgement of input through seq. The held
// reports are sent from another goroutine: the output loop calls Ack and
// must not wait behind an input send (an image upload holds the lock).
func (c *mouseCoalescer) Ack(seq uint64) {
	go func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.ackSeq = max(c.ackSeq, seq)
		if len(c.held) > 0 && c.ackSeq >= c.inFlight && !c.closed {
			if err := c.flushLocked(); err != nil && c.err == nil {
				c.err = err
			}
		}
	}()
}

// Stats reports reports received, sent, pointer moves dropped and bursts.
func (c *mouseCoalescer) Stats() (reports, sent, dropped, bursts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reports, c.sent, c.dropped, c.bursts
}
