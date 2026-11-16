package terminalstream

import (
	"strconv"
	"strings"
	"sync"

	"fennec/internal/predict"
)

// When the link to the client is slower than the agent draws (the cloud host
// sends about 128 KB/s), frames queue and the client sees every intermediate
// screen late: Codex redraws the whole screen for every mouse wheel step,
// about 4.5 KB through Herdr, so a 40-step gesture is some 180 KB.
//
// conflator sits between reading the attachment and sending its output. The
// reader keeps reading; what has not been sent yet waits in order. Frames
// that only change cells (text, cursor moves, colours the screen model keeps,
// erases, scrolls) merge into one target screen; anything else waits as raw
// bytes in its place. When the sender is free it takes everything waiting: a
// single frame goes as it was; several merged frames go as the difference
// between what the client shows and the latest screen (predict.DiffScreen,
// checked by replay), when that is smaller. Without a backlog nothing changes.
// Only the alternate screen is merged (herdr-core attach always uses it), so
// there is no scrollback to keep.
type conflator struct {
	mu      sync.Mutex
	changed *sync.Cond
	limit   int // raw bytes held before reading pauses

	scan  safetyScanner
	now   *predict.Screen // after everything read
	bound *predict.Screen // after the last complete segment
	sent  *predict.Screen // what the client shows: after everything taken
	seg   []byte          // the current, incomplete segment
	safe  bool            // the current segment only changes cells
	queue []pending
	held  int  // raw bytes in queue
	full  bool // after a resize the client may differ from sent: repaint all
	done  bool

	// Statistics: frames merged, bytes they had, bytes sent for them.
	merged, mergedBytes, diffBytes int
}

// pending is waiting output: raw bytes, or merged cell-only segments.
type pending struct {
	raw      []byte
	segments int             // > 0: merged segments ending in screen (or bound while last)
	screen   *predict.Screen // frozen end state when followed by raw bytes
}

func newConflator(rows, cols int) *conflator {
	c := &conflator{limit: 1 << 20, now: predict.NewScreen(rows, cols), bound: predict.NewScreen(rows, cols), sent: predict.NewScreen(rows, cols), safe: true}
	c.changed = sync.NewCond(&c.mu)
	return c
}

// Write takes output read from the attachment. It waits while too much raw
// output is held (the client is far behind), so memory stays bounded.
func (c *conflator) Write(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.held > c.limit && !c.done {
		c.changed.Wait()
	}
	c.scan.feed(data, func(token []byte, safe bool) {
		c.now.Write(token)
		c.seg = append(c.seg, token...)
		c.safe = c.safe && safe
		if c.now.Ground() && !c.now.Sync {
			c.closeSegment()
		}
	})
	c.changed.Broadcast()
}

// closeSegment queues the current segment: merged into the last entry when
// it only changes cells on the alternate screen, raw otherwise.
func (c *conflator) closeSegment() {
	if len(c.seg) == 0 {
		return
	}
	safe := c.safe && c.bound.AltActive && c.now.AltActive
	last := len(c.queue) - 1
	open := last >= 0 && c.queue[last].segments > 0 && c.queue[last].screen == nil
	if safe {
		if open {
			c.queue[last].raw = append(c.queue[last].raw, c.seg...)
			c.queue[last].segments++
		} else {
			c.queue = append(c.queue, pending{raw: c.seg, segments: 1})
		}
	} else {
		if open {
			// The merged entry ends where these bytes begin.
			c.queue[last].screen = c.bound.Clone()
		}
		c.queue = append(c.queue, pending{raw: c.seg})
	}
	c.bound.Write(c.seg)
	c.held += len(c.seg)
	c.seg, c.safe = nil, true
}

// Take waits for output and returns the bytes to send now, or nil when the
// stream is finished.
func (c *conflator) Take() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.queue) == 0 && !c.done {
		c.changed.Wait()
	}
	if len(c.queue) == 0 {
		return nil
	}
	var out []byte
	for _, p := range c.queue {
		switch {
		case p.segments == 0 || p.segments == 1 && !c.full:
			out = append(out, p.raw...)
			c.sent.Write(p.raw)
		default:
			target := p.screen
			if target == nil {
				target = c.bound.Clone() // the open entry is the last one
			}
			diff, ok := predict.DiffScreen(c.sent, target, c.full)
			if ok && (len(diff) < len(p.raw) || c.full) {
				out = append(out, diff...)
				c.sent = target.Clone()
				c.merged += p.segments
				c.mergedBytes += len(p.raw)
				c.diffBytes += len(diff)
				c.full = false
			} else {
				out = append(out, p.raw...)
				c.sent.Write(p.raw)
			}
		}
	}
	c.queue, c.held = nil, 0
	c.changed.Broadcast()
	return out
}

// Resize changes the screens' size. What the client shows after a resize is
// not modelled exactly, so the next merged output repaints every cell.
func (c *conflator) Resize(rows, cols int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range []*predict.Screen{c.now, c.bound, c.sent} {
		s.Resize(rows, cols)
	}
	for i := range c.queue {
		if c.queue[i].segments > 0 && c.queue[i].screen != nil {
			c.queue[i].screen.Resize(rows, cols)
		}
	}
	c.full = true
}

// Close ends the stream: what is held is still taken, then Take returns nil.
func (c *conflator) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seg) > 0 {
		// An unfinished segment goes as it is.
		c.queue = append(c.queue, pending{raw: c.seg})
		c.held += len(c.seg)
		c.seg = nil
	}
	c.done = true
	c.changed.Broadcast()
}

// Stats returns frames merged, the bytes they had and the bytes sent instead.
func (c *conflator) Stats() (merged, mergedBytes, diffBytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.merged, c.mergedBytes, c.diffBytes
}

// safetyScanner splits output into tokens (a run of text, a control
// character, an escape sequence, a string) and says whether each only
// changes cells in ways predict.Screen and DiffScreen reproduce exactly.
type safetyScanner struct {
	buf []byte // an unfinished token
}

func (z *safetyScanner) feed(data []byte, emit func(token []byte, safe bool)) {
	data = append(z.buf, data...)
	z.buf = nil
	for len(data) > 0 {
		n, safe, complete := nextToken(data)
		if !complete {
			z.buf = append([]byte(nil), data...)
			return
		}
		emit(data[:n], safe)
		data = data[n:]
	}
}

// nextToken measures the token at the start of data.
func nextToken(data []byte) (n int, safe, complete bool) {
	b := data[0]
	switch {
	case b == 0x1b:
		if len(data) < 2 {
			return 0, false, false
		}
		switch data[1] {
		case '[':
			for i := 2; i < len(data); i++ {
				if data[i] >= 0x40 && data[i] <= 0x7e {
					return i + 1, safeCSI(data[2:i], data[i]), true
				}
				if i > 64 {
					return i + 1, false, true // malformed: pass it on
				}
			}
			return 0, false, false
		case ']', 'P', '_', '^', 'X':
			// A string ends with BEL or ESC \.
			for i := 2; i < len(data); i++ {
				if data[i] == 0x07 {
					return i + 1, false, true
				}
				if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
					return i + 2, false, true
				}
				if data[i] == 0x1b && i+1 == len(data) {
					return 0, false, false
				}
			}
			return 0, false, false
		case '(', ')', '*', '+', '#', ' ', '%':
			if len(data) < 3 {
				return 0, false, false
			}
			return 3, false, true
		case 'D', 'E', 'M':
			return 2, true, true // index, next line, reverse index
		default:
			return 2, false, true
		}
	case b == '\r' || b == '\n' || b == '\b' || b == '\t':
		return 1, true, true
	case b < 0x20 || b == 0x7f:
		return 1, false, true
	}
	// A run of text up to the next control or escape.
	i := 0
	for i < len(data) && data[i] >= 0x20 && data[i] != 0x7f && data[i] != 0x1b {
		i++
	}
	return i, true, true
}

// safeCSI: cursor moves, erases, inserts and deletes, scrolls, the scroll
// region, cursor visibility, synchronized output, and SGR the screen keeps.
func safeCSI(params []byte, final byte) bool {
	p := string(params)
	if strings.HasPrefix(p, "?") {
		return (p == "?25" || p == "?2026") && (final == 'h' || final == 'l')
	}
	if strings.ContainsAny(p, "<=>!\"$'") {
		return false
	}
	switch final {
	case 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'f', 'd', 'e', 'a', '`', 'J', 'K', 'L', 'M', 'P', 'X', '@', 'S', 'T', 'r':
		return true
	case 'm':
		return safeSGR(p)
	}
	return false
}

// safeSGR: only parameters whose effect predict.Screen's pen keeps exactly
// (no underline styles or colours, double underline or rapid blink).
func safeSGR(p string) bool {
	if p == "" {
		return true
	}
	parts := strings.Split(p, ";")
	for i := 0; i < len(parts); i++ {
		sub := strings.Split(parts[i], ":")
		n, err := strconv.Atoi(sub[0])
		if sub[0] == "" {
			n, err = 0, nil
		}
		if err != nil {
			return false
		}
		switch {
		case n == 4 && len(sub) > 1:
			if sub[1] != "0" && sub[1] != "1" {
				return false
			}
		case len(sub) > 1 && n != 38 && n != 48:
			return false
		case n == 0 || n == 1 || n == 2 || n == 3 || n == 4 || n == 5 || n == 7 || n == 8 || n == 9:
		case n >= 22 && n <= 25 || n >= 27 && n <= 29:
		case n >= 30 && n <= 37 || n == 39 || n >= 40 && n <= 47 || n == 49:
		case n >= 90 && n <= 97 || n >= 100 && n <= 107:
		case n == 38 || n == 48:
			if len(sub) > 1 {
				continue // colon form, parsed by the screen as one parameter
			}
			if i+1 < len(parts) && parts[i+1] == "5" && i+2 < len(parts) {
				i += 2
			} else if i+1 < len(parts) && parts[i+1] == "2" && i+4 < len(parts) {
				i += 4
			} else {
				return false
			}
		default:
			return false
		}
	}
	return true
}
