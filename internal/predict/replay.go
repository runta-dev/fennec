package predict

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A Trace is a session recorded by FEN_PREDICT_TRACE (see Recorder): the
// server output with its timing, the user's input packets and the cloud's
// echo acknowledgements, on one clock.
type Trace struct {
	Output []TraceChunk
	Input  []TraceInput
	Acks   []TraceAck
}

// TraceChunk is one output read at At seconds.
type TraceChunk struct {
	At   float64
	Data []byte
}

// TraceInput is one input packet.
type TraceInput struct {
	At  float64
	Raw []byte
}

// TraceAck is one echo acknowledgement.
type TraceAck struct {
	At    float64
	Seq   uint64
	Quiet bool
}

// LoadTrace reads a recorded trace directory ("output", "timing", and when
// present "input" and "acks").
func LoadTrace(dir string) (Trace, error) {
	var tr Trace
	data, err := os.ReadFile(filepath.Join(dir, "output"))
	if err != nil {
		return tr, err
	}
	timing, err := traceLines(dir, "timing")
	if err != nil {
		return tr, err
	}
	at, offset := 0.0, 0
	for _, f := range timing {
		delta, err1 := strconv.ParseFloat(f[0], 64)
		n, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil || n < 0 || offset+n > len(data) {
			return tr, fmt.Errorf("%s: bad timing line %q", dir, strings.Join(f, " "))
		}
		at += delta
		tr.Output = append(tr.Output, TraceChunk{At: at, Data: data[offset : offset+n]})
		offset += n
	}
	input, err := traceLines(dir, "input")
	if err != nil {
		return tr, err
	}
	for _, f := range input {
		at, err1 := strconv.ParseFloat(f[0], 64)
		raw, err2 := hex.DecodeString(f[1])
		if err1 != nil || err2 != nil {
			return tr, fmt.Errorf("%s: bad input line %q", dir, strings.Join(f, " "))
		}
		tr.Input = append(tr.Input, TraceInput{At: at, Raw: raw})
	}
	acks, err := traceLines(dir, "acks")
	if err != nil {
		return tr, err
	}
	for _, f := range acks {
		at, err1 := strconv.ParseFloat(f[0], 64)
		seq, err2 := strconv.ParseUint(f[1], 10, 64)
		if err1 != nil || err2 != nil {
			return tr, fmt.Errorf("%s: bad ack line %q", dir, strings.Join(f, " "))
		}
		tr.Acks = append(tr.Acks, TraceAck{At: at, Seq: seq, Quiet: len(f) > 2 && f[2] == "true"})
	}
	return tr, nil
}

// traceLines returns the fields of each line with at least two; a missing
// file has none.
func traceLines(dir, name string) ([][]string, error) {
	f, err := os.Open(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][]string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) >= 2 {
			out = append(out, fields)
		}
	}
	return out, scanner.Err()
}

// ObservedState is one distinct composer state the agent drew.
type ObservedState struct {
	At     float64
	Lock   Lock
	Screen *Screen
}

// ObservedStates replays the output and records each distinct composer state
// at complete frames.
func ObservedStates(output []TraceChunk, rows, cols int, profile Profile) []ObservedState {
	s := NewScreen(rows, cols)
	var out []ObservedState
	for _, c := range output {
		s.Write(c.Data)
		if !s.Ground() {
			continue
		}
		lock, ok := profile.Locate(s)
		if !ok {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Lock.State.Equal(lock.State) && out[n-1].Lock.Top == lock.Top {
			continue
		}
		out = append(out, ObservedState{At: c.At, Lock: lock, Screen: s.Clone()})
	}
	return out
}

// ReplayReport is the result of replaying a trace through the engine.
type ReplayReport struct {
	Keys      int // input packets
	Predicted int // packets after which a prediction was shown
	Exact     int // predictions equal to a composer the agent drew later
	// Merged predictions were drawn by the agent only together with later
	// keys (a busy agent skipping a state); not wrong.
	Merged int
	// Paused counts text typed into a recognised composer and not shown.
	Paused int
	// Problems lists every prediction the agent never drew and every pause.
	Problems []string
	// Notes lists the merged predictions.
	Notes []string
	// Converged is false when the terminal did not end equal to the server
	// screen (Why says where).
	Converged bool
	Why       string
	Stats     Stats
}

// OK: every shown prediction matched, nothing paused, the terminal converged
// and at most 5% of the predictions were merged.
func (r ReplayReport) OK() bool {
	return r.Converged && len(r.Problems) == 0 && r.Exact+r.Merged == r.Predicted && r.Merged*20 <= r.Predicted
}

// Replay drives the engine with a recorded session: keys, output and
// acknowledgements at their recorded times (so with the real link latency).
// Each prediction shown must match a composer the agent later drew (the whole
// screen when fullScreen), and text typed into a recognised composer must be
// shown at once. profile must return a new profile on each call.
func Replay(tr Trace, rows, cols int, profile func() Profile, fullScreen bool) ReplayReport {
	chunks, keys, acks := tr.Output, tr.Input, tr.Acks
	observed := ObservedStates(chunks, rows, cols, profile())
	// One checker keeps what a profile remembers between frames (as the
	// engine's does), e.g. OpenCode's block top behind a popup.
	checker := profile()
	now := time.Unix(0, 0)
	at := func(sec float64) time.Time { return time.Unix(0, int64(sec*1e9)) }
	var term bytes.Buffer
	engine := New(&term, profile(), rows, cols, Options{noTick: true, Mode: ModeAlways, EchoAck: len(acks) > 0, Now: func() time.Time { return now }})
	engine.trusted = true
	defer engine.Close()
	res := ReplayReport{Keys: len(keys)}
	problem := func(format string, args ...any) { res.Problems = append(res.Problems, fmt.Sprintf(format, args...)) }
	ci, ai := 0, 0
	var split PasteSplitter // classifies keys like the engine (a paste split across reads)
	advance := func(until float64) {
		for {
			nextChunk, nextAck := 1e18, 1e18
			if ci < len(chunks) {
				nextChunk = chunks[ci].At
			}
			if ai < len(acks) {
				nextAck = acks[ai].At
			}
			if (ci == len(chunks) && ai == len(acks)) || min(nextChunk, nextAck) > until {
				return
			}
			if nextChunk <= nextAck {
				now = at(nextChunk)
				engine.Server(chunks[ci].Data)
				ci++
			} else {
				now = at(nextAck)
				engine.Ack(acks[ai].Seq, acks[ai].Quiet)
				ai++
			}
		}
	}
	for _, k := range keys {
		advance(k.At)
		now = at(k.At)
		engine.mu.Lock()
		locked := engine.locked && (len(engine.pending) == 0 || !engine.pending[len(engine.pending)-1].blocked())
		engine.mu.Unlock()
		barriers := engine.Stats().Barriers
		engine.Input(k.Raw)
		barrier := engine.Stats().Barriers > barriers
		// A key typed while a server frame is half received is drawn when
		// the frame completes (the engine never writes into a frame); judge
		// the screen there.
		for {
			engine.mu.Lock()
			ground := engine.shadow.Ground()
			engine.mu.Unlock()
			if ground || ci >= len(chunks) {
				break
			}
			now = at(chunks[ci].At)
			engine.Server(chunks[ci].Data)
			ci++
		}
		engine.mu.Lock()
		shown := engine.display.Clone()
		painted := len(engine.painted) > 0
		engine.mu.Unlock()
		typed := false
		for _, key := range split.Keys(k.Raw) {
			typed = typed || key.Kind == KeyText
		}
		if !painted {
			if locked && typed && !barrier {
				res.Paused++
				problem("text %q at %.3f was typed into a locked composer but not shown", k.Raw, k.At)
			}
			continue
		}
		res.Predicted++
		mine, ok := checker.Locate(shown)
		if !ok {
			problem("key %q at %.3f: prediction is not a composer\n%s", k.Raw, k.At, DumpScreen(shown))
			continue
		}
		matched := false
		for _, o := range observed {
			if o.At <= k.At || !checker.Same(mine.State, o.Lock.State, o.Screen) {
				continue
			}
			equal := composerRowsEqual(shown, o.Screen, o.Lock)
			if fullScreen {
				equal = DumpScreen(shown) == DumpScreen(o.Screen)
			}
			if !equal {
				problem("key %q at %.3f: shown screen differs from the agent's frame (lock rows %d..%d, cursor %d,%d vs %d,%d):\n%s", k.Raw, k.At, o.Lock.Top, o.Lock.Bottom, shown.Row, shown.Col, o.Screen.Row, o.Screen.Col, rowDiff(shown, o.Screen))
			}
			matched = true
			break
		}
		if matched {
			res.Exact++
			continue
		}
		// A busy agent may draw several keys in one frame, skipping this
		// state; then its next new state must continue it (the prediction
		// was a step on the way, not wrong).
		next := State{}
		for _, o := range observed {
			if o.At > k.At && len(o.Lock.State.Elems) >= len(mine.State.Elems) && !o.Lock.State.Equal(mine.State) {
				next = o.Lock.State
				break
			}
		}
		if strings.HasPrefix(next.Text(), mine.State.Text()) && next.Cursor > mine.State.Cursor && len(mine.State.Text()) == mine.State.Cursor {
			res.Merged++
			res.Notes = append(res.Notes, fmt.Sprintf("key %q at %.3f: drawn only together with later keys (%s, then %s)", k.Raw, k.At, describe(mine.State), describe(next)))
			continue
		}
		problem("key %q at %.3f: agent never showed the predicted %s", k.Raw, k.At, describe(mine.State))
	}
	advance(1e18)
	now = now.Add(5 * time.Second)
	engine.Ack(0, false)
	engine.mu.Lock()
	res.Converged, res.Why = gridEqual(engine.display, engine.shadow)
	engine.mu.Unlock()
	res.Stats = engine.Stats()
	return res
}

// DumpScreen is the screen's text, one line per row without trailing spaces.
func DumpScreen(s *Screen) string {
	var b strings.Builder
	for r := 0; r < s.Rows; r++ {
		b.WriteString(strings.TrimRight(s.RowText(r, 0, s.Cols), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func describe(st State) string { return fmt.Sprintf("%q@%d", st.Text(), st.Cursor) }

// composerRowsEqual compares the composer rows and cursor of two screens.
func composerRowsEqual(a, b *Screen, lock Lock) bool {
	for r := max(0, lock.Top-1); r <= min(a.Rows-1, lock.Bottom+1); r++ {
		if strings.TrimRight(a.RowText(r, 0, a.Cols), " ") != strings.TrimRight(b.RowText(r, 0, b.Cols), " ") {
			return false
		}
	}
	return a.Row == b.Row && a.Col == b.Col
}

// rowDiff lists the rows whose text differs between two screens.
func rowDiff(a, b *Screen) string {
	var out strings.Builder
	for r := 0; r < a.Rows; r++ {
		x, y := strings.TrimRight(a.RowText(r, 0, a.Cols), " "), strings.TrimRight(b.RowText(r, 0, b.Cols), " ")
		if x != y {
			fmt.Fprintf(&out, "  row %2d shown |%s|\n  row %2d agent |%s|\n", r, x, r, y)
		}
	}
	return out.String()
}

// gridEqual compares every cell and the cursor.
func gridEqual(a, b *Screen) (bool, string) {
	for r := 0; r < a.Rows; r++ {
		for c := 0; c < a.Cols; c++ {
			if a.Cell(r, c) != b.Cell(r, c) {
				return false, fmt.Sprintf("cell %d,%d: terminal %+v server %+v", r, c, a.Cell(r, c), b.Cell(r, c))
			}
		}
	}
	if a.Row != b.Row || a.Col != b.Col {
		return false, fmt.Sprintf("cursor terminal %d,%d server %d,%d", a.Row, a.Col, b.Row, b.Col)
	}
	return true, ""
}
