package predict

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Mode selects when predictions are shown.
type Mode uint8

const (
	ModeAdaptive Mode = iota
	ModeAlways
	ModeOff
	ModeUnderline // always, with predicted cells underlined (manual verification)
)

// ParseMode accepts the setting and environment values.
func ParseMode(value string) (Mode, bool) {
	switch value {
	case "", "adaptive", "on", "1", "true":
		return ModeAdaptive, true
	case "always":
		return ModeAlways, true
	case "off", "0", "false":
		return ModeOff, true
	case "underline":
		return ModeUnderline, true
	}
	return ModeAdaptive, false
}

const (
	// Deadline is how long an unconfirmed edit may stay on screen.
	Deadline = 1500 * time.Millisecond
	// BusyDeadline applies when the server reported the agent produced no
	// output after the key (it is busy, not refusing).
	BusyDeadline = 3 * time.Second
	// trustDelay is how long after start or resize the engine assumes the
	// server has redrawn the screen even without an explicit clear.
	trustDelay = 1500 * time.Millisecond
	// barrierFallback resolves a barrier without echo acknowledgements.
	barrierFallback = 400 * time.Millisecond
	// ackGrace: an acknowledgement only says the agent produced some output
	// after the key (Codex emits a cursor frame at once and draws the echo up
	// to 17 ms later; Ink throttles renders). The echo itself may still be on
	// its way, so a prediction is judged only this long after the ack.
	ackGrace = 250 * time.Millisecond
)

// Stats are cumulative counters for diagnostics.
type Stats struct {
	Keys, Predicted, Confirmed, Diverged, Expired, Barriers, Unlocked int
	ConfirmP50, ConfirmP95                                            time.Duration
	// LocalPaint includes input-lock contention and the terminal write. It is
	// separate from cloud confirmation and uses wall time, not the trace clock.
	LocalPaintP50, LocalPaintP95 time.Duration
}

type edit struct {
	key        Key
	seq        uint64
	at         time.Time
	apply      EditFunc // nil: barrier, or deferred
	image      bool
	imageReady bool
	dropped    bool
	deferred   bool  // typed behind a barrier; mapped when it resolves
	before     State // for a barrier: the composer it was typed into
}

// Options configure an engine.
type Options struct {
	Mode Mode
	// EchoAck is true when the server sends echo_ack frames.
	EchoAck bool
	// ImageReady reports whether the clipboard holds an image; called on the
	// input goroutine when the image paste shortcut is typed.
	ImageReady func() bool
	Now        func() time.Time
	// noTick disables the background timer (tests drive time themselves).
	noTick bool
	// Record, when set, receives every server chunk and typed packet.
	Record *Recorder
}

// Engine renders composer predictions on top of passthrough output.
type Engine struct {
	mu      sync.Mutex
	out     io.Writer
	profile Profile
	opts    Options

	shadow  *Screen // the server's screen
	display *Screen // what the user's terminal shows
	trusted bool
	since   time.Time

	locked     bool
	lock       Lock
	confirmed  State
	pending    []edit
	seq        uint64
	ackSeq     uint64
	ackQuiet   bool // the last ack reported no output
	ackAt      time.Time
	pastes     PasteSplitter
	imagePaste bool

	painted      map[int]bool
	cursorDirt   bool
	srtt         time.Duration
	samples      []time.Duration
	paintSamples []time.Duration
	stats        Stats

	stop chan struct{}
	done chan struct{}
}

// New creates an engine writing to out. profile may be nil, in which case the
// engine only passes output through.
func New(out io.Writer, profile Profile, rows, cols int, opts Options) *Engine {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	e := &Engine{out: out, profile: profile, opts: opts, shadow: NewScreen(rows, cols), painted: map[int]bool{}, stop: make(chan struct{}), done: make(chan struct{})}
	e.display = e.shadow.Clone()
	e.since = opts.Now()
	if profile == nil || opts.Mode == ModeOff || opts.noTick {
		close(e.done)
		return e
	}
	go e.tick()
	return e
}

func (e *Engine) active() bool { return e.profile != nil && e.opts.Mode != ModeOff }

// Active reports whether the engine predicts at all (a profile exists for the
// agent and prediction is not off).
func (e *Engine) Active() bool { return e.active() }

func (e *Engine) tick() {
	defer close(e.done)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-ticker.C:
			e.mu.Lock()
			if len(e.pending) > 0 || !e.trusted || len(e.painted) > 0 {
				var buf bytes.Buffer
				e.settle(&buf)
				e.flush(&buf)
			}
			e.mu.Unlock()
		}
	}
}

// SetImagePaste records whether the server accepts image uploads.
func (e *Engine) SetImagePaste(on bool) { e.mu.Lock(); e.imagePaste = on; e.mu.Unlock() }

// SetEchoAck records whether the server acknowledges processed input.
func (e *Engine) SetEchoAck(on bool) { e.mu.Lock(); e.opts.EchoAck = on; e.mu.Unlock() }

// Server passes server output to the terminal and updates predictions.
func (e *Engine) Server(data []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.opts.Record.Output(data)
	if !e.active() {
		_, err := e.out.Write(data)
		return err
	}
	var buf bytes.Buffer
	e.restore(&buf)
	buf.Write(data)
	e.shadow.Write(data)
	e.display.Write(data)
	if e.shadow.Scrolled {
		e.shadow.Scrolled = false
		e.display.Scrolled = false
	}
	e.settle(&buf)
	_, err := e.out.Write(buf.Bytes())
	return err
}

// Input records keys typed by the user and paints their prediction. It
// returns the input sequence number before data; the caller sends data with
// that base so acknowledgements can be matched.
func (e *Engine) Input(data []byte) uint64 {
	started := time.Now()
	e.opts.Record.Input(data)
	keys := e.pastes.Keys(data) // only the input goroutine calls Input
	// The clipboard check runs a helper process; never hold the lock for it.
	imageReady := false
	e.mu.Lock()
	wantImage := e.active() && e.locked && e.imagePaste && e.opts.ImageReady != nil
	e.mu.Unlock()
	if wantImage {
		for _, k := range keys {
			if k.Kind == KeyImagePaste {
				imageReady = e.opts.ImageReady()
				break
			}
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	predictedBefore := e.stats.Predicted
	defer func() {
		if e.stats.Predicted > predictedBefore && len(e.painted) > 0 {
			e.paintSamples = append(e.paintSamples, time.Since(started))
			if len(e.paintSamples) > 512 {
				e.paintSamples = e.paintSamples[len(e.paintSamples)-512:]
			}
		}
	}()
	base := e.seq
	e.seq += uint64(len(data))
	if !e.active() {
		return base
	}
	now := e.opts.Now()
	offset := base
	for _, k := range keys {
		offset += uint64(k.Bytes)
		e.stats.Keys++
		if k.Kind == KeyNeutral {
			continue
		}
		if !e.locked {
			continue
		}
		ed := edit{key: k, seq: offset, at: now, imageReady: k.Kind == KeyImagePaste && imageReady && e.imagePaste}
		if n := len(e.pending); n > 0 && e.pending[n-1].blocked() {
			// Typed behind an unpredictable key: mapped once the server
			// shows what that key did.
			ed.deferred = true
		} else {
			e.mapEdit(&ed, e.predicted())
		}
		e.pending = append(e.pending, ed)
	}
	var buf bytes.Buffer
	e.settle(&buf)
	e.flush(&buf)
	return base
}

func (e *Engine) mapEdit(ed *edit, st State) {
	ed.deferred = false
	ed.apply = e.profile.Edit(ed.key, st, EditContext{Pending: len(e.pending), ImageReady: ed.imageReady})
	ed.image = ed.key.Kind == KeyImagePaste && ed.apply != nil
	if ed.apply == nil {
		ed.before = st
		e.stats.Barriers++
	} else {
		e.stats.Predicted++
	}
}

// mapDeferred maps edits typed behind a resolved barrier against the server
// state, up to the next unpredictable key.
func (e *Engine) mapDeferred() {
	st := e.confirmed
	for i := range e.pending {
		ed := &e.pending[i]
		if ed.deferred {
			e.mapEdit(ed, st)
		}
		if ed.barrier() {
			return
		}
		if !ed.dropped {
			st = ed.apply(st)
		}
	}
}

// barrier: the key's outcome is unknown until the server shows it.
func (ed edit) barrier() bool { return ed.apply == nil && !ed.deferred }

// blocked: no prediction can be shown past this edit yet.
func (ed edit) blocked() bool { return ed.apply == nil }

// Ack records that the server processed input through seq. quiet is true when
// the agent produced no output in response (it is busy).
func (e *Engine) Ack(seq uint64, quiet bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.opts.Record.Ack(seq, quiet)
	if seq > e.ackSeq {
		e.ackSeq, e.ackQuiet, e.ackAt = seq, quiet, e.opts.Now()
	}
	var buf bytes.Buffer
	e.settle(&buf)
	e.flush(&buf)
}

// RejectImage removes the oldest pending image prediction after a failed
// upload or validation.
func (e *Engine) RejectImage() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.pending {
		if e.pending[i].image && !e.pending[i].dropped {
			e.pending[i].dropped = true
			break
		}
	}
	var buf bytes.Buffer
	e.settle(&buf)
	e.flush(&buf)
}

// Resize discards predictions; the server redraws after a resize.
func (e *Engine) Resize(rows, cols int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shadow.Resize(rows, cols)
	e.display.Resize(rows, cols)
	e.pending, e.painted, e.locked = nil, map[int]bool{}, false
	e.trusted, e.since = false, e.opts.Now()
}

// Close restores every predicted cell and stops the timer.
func (e *Engine) Close() {
	e.mu.Lock()
	if e.active() {
		e.pending = nil
		var buf bytes.Buffer
		if e.shadow.Ground() && e.trusted {
			e.paint(&buf)
		}
		e.restore(&buf)
		e.flush(&buf)
		select {
		case <-e.stop:
		default:
			close(e.stop)
		}
	}
	e.mu.Unlock()
	<-e.done
	e.opts.Record.Close()
}

// Stats returns a copy of the counters.
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.stats
	if n := len(e.paintSamples); n > 0 {
		sorted := append([]time.Duration(nil), e.paintSamples...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		s.LocalPaintP50, s.LocalPaintP95 = sorted[n/2], sorted[min(n-1, n*95/100)]
	}
	if n := len(e.samples); n > 0 {
		sorted := append([]time.Duration(nil), e.samples...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		s.ConfirmP50 = sorted[n/2]
		s.ConfirmP95 = sorted[min(n-1, n*95/100)]
	}
	return s
}

func (e *Engine) flush(buf *bytes.Buffer) {
	if buf.Len() > 0 {
		e.out.Write(buf.Bytes())
		buf.Reset()
	}
}

// predicted applies pending edits up to the first barrier.
func (e *Engine) predicted() State {
	st := e.confirmed
	for _, ed := range e.pending {
		if ed.blocked() {
			break
		}
		if ed.dropped {
			continue
		}
		st = ed.apply(st)
	}
	return st
}

// settle reconciles with the server screen and repaints when the frame is
// complete.
func (e *Engine) settle(buf *bytes.Buffer) {
	if !e.shadow.Ground() {
		return
	}
	now := e.opts.Now()
	if !e.trusted {
		if now.Sub(e.since) < trustDelay {
			return
		}
		e.trusted = true
		e.display = e.shadow.Clone()
		e.painted = map[int]bool{}
	}
	e.reconcile(now)
	e.paint(buf)
}

func (e *Engine) reconcile(now time.Time) {
	lock, ok := e.profile.Locate(e.shadow)
	if !ok && e.locked {
		// The screen may be ambiguous yet exactly explained by the model:
		// the confirmed state or a prefix of the predicted edits.
		st := e.confirmed
		lock, ok = e.profile.Match(e.shadow, st)
		for _, ed := range e.pending {
			if ok || ed.blocked() {
				break
			}
			if !ed.dropped {
				st = ed.apply(st)
			}
			lock, ok = e.profile.Match(e.shadow, st)
		}
	}
	if !ok {
		if e.locked {
			e.stats.Unlocked++
		}
		e.locked = false
		e.pending = nil
		return
	}
	server := lock.State
	e.lock = lock
	if !e.locked {
		e.locked, e.confirmed, e.pending = true, server, nil
		return
	}
	for {
		if len(e.pending) == 0 {
			e.confirmed = e.adopt(e.confirmed, server)
			return
		}
		// Longest prefix of predicted edits that reproduces the server state.
		st, best, bestState := e.confirmed, -1, State{}
		if e.profile.Same(st, server, e.shadow) {
			best, bestState = 0, st
		}
		for i, ed := range e.pending {
			if ed.blocked() {
				break
			}
			if !ed.dropped {
				st = ed.apply(st)
			}
			if e.profile.Same(st, server, e.shadow) {
				best, bestState = i+1, st
			}
		}
		if best > 0 {
			for _, ed := range e.pending[:best] {
				if !ed.dropped {
					e.stats.Confirmed++
					e.sample(now.Sub(ed.at))
				}
			}
			e.pending = e.pending[best:]
			// The server's parse is authoritative (token numbers, spacing),
			// except for trailing spaces the screen cannot show.
			// bestState proved the edits were applied.
			e.confirmed = e.adopt(bestState, server)
			continue
		}
		first := e.pending[0]
		if first.barrier() {
			if e.barrierResolved(first, server, now) {
				e.pending = e.pending[1:]
				e.confirmed = server
				e.mapDeferred()
				continue
			}
			if best == 0 {
				return
			}
		}
		if best == 0 {
			// Nothing processed yet, unless the server says otherwise.
			if !e.expired(first, now) {
				return
			}
		} else if !e.diverged(first, now) {
			return
		}
		e.rollback(server)
		return
	}
}

// barrierResolved: the server has shown what the unpredictable key did. An
// acknowledgement alone is not enough (the agent may answer with a small
// frame first and redraw the composer later, as Claude Code does for "!"):
// the composer must have changed, or the grace period passed with no change.
func (e *Engine) barrierResolved(ed edit, server State, now time.Time) bool {
	if e.opts.EchoAck {
		if e.ackSeq >= ed.seq && (!e.profile.Same(server, ed.before, e.shadow) || now.Sub(e.ackAt) >= ackGrace) {
			return true
		}
		return now.Sub(ed.at) > BusyDeadline
	}
	wait := barrierFallback
	if e.srtt > 0 {
		wait = max(wait, e.srtt*3/2)
	}
	return now.Sub(ed.at) > wait
}

// adopt returns the state to confirm when model explains the server's screen:
// the server's reading (token numbers, spacing), except for trailing spaces
// the screen cannot show, which only the model knows.
func (e *Engine) adopt(model, server State) State {
	if t := model.Text(); t != server.Text() && strings.TrimRight(t, " ") == server.Text() && e.profile.Same(model, server, e.shadow) {
		return model
	}
	return server
}

// ackedSettled: the server acknowledged ed (with output) long enough ago that
// its echo, if any, has arrived.
func (e *Engine) ackedSettled(ed edit, now time.Time) bool {
	return e.opts.EchoAck && e.ackSeq >= ed.seq && !e.ackQuiet && now.Sub(e.ackAt) >= ackGrace
}

// rollback adopts the server's state after a misprediction. Keys the server
// has not acknowledged are still on their way to the agent: they stay
// predicted, remapped onto the server's state, instead of vanishing.
func (e *Engine) rollback(server State) {
	e.stats.Diverged++
	e.confirmed = server
	var keep []edit
	if e.opts.EchoAck {
		for _, ed := range e.pending {
			if ed.seq > e.ackSeq {
				ed.apply, ed.image, ed.deferred = nil, false, true
				keep = append(keep, ed)
			}
		}
	}
	e.pending = keep
	e.mapDeferred()
}

// expired: the screen still shows the old state.
func (e *Engine) expired(ed edit, now time.Time) bool {
	if e.ackedSettled(ed, now) {
		// The agent answered the key and still does not show it.
		e.stats.Expired++
		return true
	}
	limit := Deadline
	if e.opts.EchoAck && e.ackQuiet {
		limit = BusyDeadline
	}
	if now.Sub(ed.at) > limit {
		e.stats.Expired++
		return true
	}
	return false
}

// diverged: the screen shows something that no prefix explains.
func (e *Engine) diverged(ed edit, now time.Time) bool {
	if e.opts.EchoAck {
		return e.ackedSettled(ed, now) || now.Sub(ed.at) > Deadline
	}
	// Without acknowledgements an intermediate frame (for example a frame
	// rendered before the latest key arrived) can look like divergence; wait
	// for the deadline or a round trip.
	wait := Deadline / 3
	if e.srtt > 0 {
		wait = max(e.srtt*2, 150*time.Millisecond)
	}
	return now.Sub(ed.at) > min(wait, Deadline)
}

func (e *Engine) sample(d time.Duration) {
	if d <= 0 {
		return
	}
	if e.srtt == 0 {
		e.srtt = d
	} else {
		e.srtt = (7*e.srtt + d) / 8
	}
	e.samples = append(e.samples, d)
	if len(e.samples) > 512 {
		e.samples = e.samples[len(e.samples)-512:]
	}
}

func (e *Engine) showing() bool {
	switch e.opts.Mode {
	case ModeOff:
		return false
	case ModeAlways, ModeUnderline:
		return true
	}
	// Adaptive: hide only once the link has proven fast.
	return !(len(e.samples) >= 5 && e.srtt < 20*time.Millisecond)
}

// paint brings the display to shadow + prediction.
func (e *Engine) paint(buf *bytes.Buffer) {
	desired := map[int][]Cell{}
	cursorRow, cursorCol, predicting := e.shadow.Row, e.shadow.Col, false
	if e.locked && e.showing() && len(e.pending) > 0 && e.canDraw() {
		st := e.predicted()
		if !e.profile.Same(st, e.lock.State, e.shadow) {
			if p, ok := e.profile.Render(e.lock, st, e.shadow); ok {
				desired, cursorRow, cursorCol, predicting = p.Rows, p.CursorRow, p.CursorCol, true
			}
		}
	}
	rows := map[int]bool{}
	for r := range desired {
		rows[r] = true
	}
	for r := range e.painted {
		rows[r] = true
	}
	order := make([]int, 0, len(rows))
	for r := range rows {
		if r >= 0 && r < e.shadow.Rows {
			order = append(order, r)
		}
	}
	sort.Ints(order)
	var out bytes.Buffer
	pen, penKnown := Attr{}, false
	for _, r := range order {
		want, ok := desired[r]
		if !ok {
			want = e.shadow.grid[r]
		}
		have := e.display.grid[r]
		for c := 0; c < e.shadow.Cols; {
			if c < len(want) && want[c] == have[c] {
				c++
				continue
			}
			start := c
			if start > 0 && want[start].Width == 0 {
				start--
			}
			end := c
			for end < e.shadow.Cols && end < len(want) && (want[end] != have[end] || (end+1 < len(want) && want[end+1].Width == 0)) {
				end++
			}
			if out.Len() == 0 {
				e.beginPaint(&out)
			}
			fmt.Fprintf(&out, "\x1b[%d;%dH", r+1, start+1)
			for col := start; col < end && col < len(want); col++ {
				cell := want[col]
				if cell.Width == 0 {
					continue
				}
				attr := cell.Attr
				if predicting && e.opts.Mode == ModeUnderline && desired[r] != nil {
					attr.Flags |= AttrUnderline
				}
				if !penKnown || attr != pen {
					out.WriteString(SGR(attr))
					pen, penKnown = attr, true
				}
				out.WriteString(cell.Glyph())
			}
			c = max(end, c+1)
		}
	}
	needCursor := predicting && (e.display.Row != cursorRow || e.display.Col != cursorCol || out.Len() > 0)
	if !predicting && (out.Len() > 0 || e.cursorDirt) && (e.display.Row != e.shadow.Row || e.display.Col != e.shadow.Col) {
		needCursor = true
	}
	if needCursor {
		if out.Len() == 0 {
			e.beginPaint(&out)
		}
		fmt.Fprintf(&out, "\x1b[%d;%dH", cursorRow+1, cursorCol+1)
		if predicting && !e.display.CursorVisible {
			out.WriteString("\x1b[?25h")
		}
	}
	if out.Len() > 0 {
		out.WriteString("\x1b[?2026l")
		e.display.Write(out.Bytes())
		buf.Write(out.Bytes())
		e.cursorDirt = true
	}
	e.painted = map[int]bool{}
	for r := range desired {
		e.painted[r] = true
	}
	if !predicting && len(e.painted) == 0 {
		e.restore(buf)
	}
}

// canDraw reports whether injecting bytes now keeps the terminal consistent.
func (e *Engine) canDraw() bool {
	s := e.shadow
	// The cursor may be hidden: profiles only lock where that is the agent's
	// normal state (Grok hides it at the end of an exactly full row).
	return s.Ground() && !s.OriginMode && !s.PendingWrap
}

func (e *Engine) beginPaint(out *bytes.Buffer) {
	out.WriteString("\x1b[?2026h")
	if e.display.InsertMode {
		out.WriteString("\x1b[4l")
	}
	if e.display.ShiftOut {
		out.WriteString("\x0f")
	}
	if e.display.G0Graphics {
		out.WriteString("\x1b(B")
	}
}

// restore returns cursor, pen and modes to the server's values so the next
// server bytes behave exactly as the server expects.
func (e *Engine) restore(buf *bytes.Buffer) {
	if !e.cursorDirt {
		return
	}
	s, d := e.shadow, e.display
	var out bytes.Buffer
	if s.PendingWrap {
		last := s.Cell(s.Row, s.Cols-1)
		fmt.Fprintf(&out, "\x1b[%d;%dH%s%s", s.Row+1, s.Cols, SGR(last.Attr), last.Glyph())
	} else if d.Row != s.Row || d.Col != s.Col || d.PendingWrap {
		fmt.Fprintf(&out, "\x1b[%d;%dH", s.Row+1, s.Col+1)
	}
	out.WriteString(SGR(s.Pen))
	if s.G0Graphics != d.G0Graphics {
		if s.G0Graphics {
			out.WriteString("\x1b(0")
		} else {
			out.WriteString("\x1b(B")
		}
	}
	if s.ShiftOut != d.ShiftOut {
		if s.ShiftOut {
			out.WriteString("\x0e")
		} else {
			out.WriteString("\x0f")
		}
	}
	if s.InsertMode != d.InsertMode {
		if s.InsertMode {
			out.WriteString("\x1b[4h")
		} else {
			out.WriteString("\x1b[4l")
		}
	}
	if s.CursorVisible != d.CursorVisible {
		if s.CursorVisible {
			out.WriteString("\x1b[?25h")
		} else {
			out.WriteString("\x1b[?25l")
		}
	}
	e.display.Write(out.Bytes())
	buf.Write(out.Bytes())
	e.cursorDirt = false
}
