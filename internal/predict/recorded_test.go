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
	"testing"
	"time"
)

type recordedKey struct {
	at  float64
	raw []byte
}

type recordedAck struct {
	at    float64
	seq   uint64
	quiet bool
}

// loadInput reads a recorder's "input" file: "<seconds> <hex>" per packet, on
// the same clock as the output timing.
func loadInput(t *testing.T, name string) []recordedKey {
	t.Helper()
	var keys []recordedKey
	for _, fields := range loadLines(t, name, "input") {
		at, _ := strconv.ParseFloat(fields[0], 64)
		raw, err := hex.DecodeString(fields[1])
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, recordedKey{at: at, raw: raw})
	}
	return keys
}

// loadAcks reads the cloud's echo acknowledgements, if the recording has them.
func loadAcks(t *testing.T, name string) []recordedAck {
	t.Helper()
	var acks []recordedAck
	for _, fields := range loadLines(t, name, "acks") {
		at, _ := strconv.ParseFloat(fields[0], 64)
		seq, _ := strconv.ParseUint(fields[1], 10, 64)
		acks = append(acks, recordedAck{at: at, seq: seq, quiet: len(fields) > 2 && fields[2] == "true"})
	}
	return acks
}

func loadLines(t *testing.T, name, file string) [][]string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "traces", name, file))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][]string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) >= 2 {
			out = append(out, fields)
		}
	}
	return out
}

type replayResult struct {
	keys, predicted, exact, paused int
	merged                         int // drawn by the agent only together with later keys
	stats                          Stats
}

// replayRecorded drives the engine with a real session recorded through fen:
// keys, output and acknowledgements at their recorded times (so with the
// real link latency). Each prediction shown must match a composer the agent
// later drew (the whole screen when fullScreen), and text typed into a locked
// composer must always be shown at once.
func replayRecorded(t *testing.T, name string, rows, cols int, profile func() Profile, fullScreen bool) replayResult {
	t.Helper()
	chunks := loadTrace(t, name)
	keys := loadInput(t, name)
	acks := loadAcks(t, name)
	observedStates := composerStates(t, name, rows, cols, profile())
	// One checker keeps what a profile remembers between frames (as the
	// engine's does), e.g. OpenCode's block top behind a popup.
	checker := profile()
	now := time.Unix(0, 0)
	at := func(sec float64) time.Time { return time.Unix(0, int64(sec*1e9)) }
	var term bytes.Buffer
	engine := New(&term, profile(), rows, cols, Options{noTick: true, Mode: ModeAlways, EchoAck: len(acks) > 0, Now: func() time.Time { return now }})
	engine.trusted = true
	defer engine.Close()
	var res replayResult
	res.keys = len(keys)
	ci, ai := 0, 0
	var split PasteSplitter // classifies keys like the engine (a paste split across reads)
	advance := func(until float64) {
		for {
			nextChunk, nextAck := 1e18, 1e18
			if ci < len(chunks) {
				nextChunk = chunks[ci].At
			}
			if ai < len(acks) {
				nextAck = acks[ai].at
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
				engine.Ack(acks[ai].seq, acks[ai].quiet)
				ai++
			}
		}
	}
	for _, k := range keys {
		advance(k.at)
		now = at(k.at)
		engine.mu.Lock()
		locked := engine.locked && len(engine.pending) == 0 || (engine.locked && !engine.pending[len(engine.pending)-1].blocked())
		engine.mu.Unlock()
		barriers := engine.Stats().Barriers
		engine.Input(k.raw)
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
		for _, key := range split.Keys(k.raw) {
			typed = typed || key.Kind == KeyText
		}
		if !painted {
			if locked && typed && !barrier {
				res.paused++
				t.Errorf("text %q at %.3f was typed into a locked composer but not shown", k.raw, k.at)
			}
			continue
		}
		res.predicted++
		mine, ok := checker.Locate(shown)
		if !ok {
			t.Errorf("key %q at %.3f: prediction is not a composer\n%s", k.raw, k.at, dumpScreen(shown))
			continue
		}
		matched := false
		for _, o := range observedStates {
			if o.at <= k.at || !checker.Same(mine.State, o.lock.State, o.screen) {
				continue
			}
			equal := composerRowsEqual(shown, o.screen, o.lock)
			if fullScreen {
				equal = dumpScreen(shown) == dumpScreen(o.screen)
			}
			if !equal {
				t.Errorf("key %q at %.3f: shown screen differs from the agent's frame (lock rows %d..%d, cursor %d,%d vs %d,%d):\n%s", k.raw, k.at, o.lock.Top, o.lock.Bottom, shown.Row, shown.Col, o.screen.Row, o.screen.Col, rowDiff(shown, o.screen))
			}
			matched = true
			break
		}
		if matched {
			res.exact++
			continue
		}
		// A busy agent may draw several keys in one frame, skipping this
		// state; then its next new state must continue it (the prediction
		// was a step on the way, not wrong).
		next := State{}
		for _, o := range observedStates {
			if o.at > k.at && len(o.lock.State.Elems) >= len(mine.State.Elems) && !o.lock.State.Equal(mine.State) {
				next = o.lock.State
				break
			}
		}
		if strings.HasPrefix(next.Text(), mine.State.Text()) && next.Cursor > mine.State.Cursor && len(mine.State.Text()) == mine.State.Cursor {
			res.merged++
			t.Logf("key %q at %.3f: drawn only together with later keys (%s, then %s)", k.raw, k.at, describe(mine.State), describe(next))
			continue
		}
		t.Errorf("key %q at %.3f: agent never showed the predicted %s", k.raw, k.at, describe(mine.State))
	}
	advance(1e18)
	now = now.Add(5 * time.Second)
	engine.Ack(0, false)
	engine.mu.Lock()
	ok, why := gridEqual(engine.display, engine.shadow)
	engine.mu.Unlock()
	if !ok {
		t.Fatalf("terminal did not converge: %s", why)
	}
	res.stats = engine.Stats()
	t.Logf("local paint p50=%v p95=%v (wall time, separate from recorded cloud confirmations)", res.stats.LocalPaintP50, res.stats.LocalPaintP95)
	if os.Getenv("FENNEC_TUI_PAINT_BUDGET") == "1" && res.stats.LocalPaintP95 >= 50*time.Millisecond {
		t.Errorf("local paint P95 %v exceeds 50 ms", res.stats.LocalPaintP95)
	}
	t.Logf("keys=%d predicted=%d exact=%d merged=%d paused=%d confirmed=%d diverged=%d expired=%d p50=%v p95=%v", res.keys, res.predicted, res.exact, res.merged, res.paused, res.stats.Confirmed, res.stats.Diverged, res.stats.Expired, res.stats.ConfirmP50, res.stats.ConfirmP95)
	if res.exact+res.merged != res.predicted || res.merged*20 > res.predicted {
		t.Fatalf("%d of %d shown predictions matched the agent", res.exact, res.predicted)
	}
	return res
}

// TestCodexRecordedSession replays a real Codex session (typing, wrapping,
// Shift+Enter, Backspace, Left, insertion, bracketed paste).
func TestCodexRecordedSession(t *testing.T) {
	res := replayRecorded(t, "codex-100x30", 30, 100, func() Profile { return newCodexProfile() }, false)
	if res.predicted < res.keys/2 {
		t.Fatalf("only %d of %d key packets predicted", res.predicted, res.keys)
	}
}

// TestClaudeTranscriptRecordedSession replays a real Claude Code session with
// a full transcript above the composer: typing that wraps twice, Shift+Enter
// (the hint row appears) and Backspace back to two rows. Each growth scrolls
// the transcript up and each shrink scrolls it back; the whole predicted
// screen must equal Claude Code's frame, with the cloud's real
// acknowledgements.
func TestClaudeTranscriptRecordedSession(t *testing.T) {
	res := replayRecorded(t, "claude-transcript-100x30", 30, 100, func() Profile { return newClaudeProfile() }, true)
	if res.stats.Diverged != 0 {
		t.Fatalf("%d rollbacks in a session where every prediction was right", res.stats.Diverged)
	}
	if res.predicted < 150 {
		t.Fatalf("only %d of %d key packets predicted", res.predicted, res.keys)
	}
}

// TestClaudeStreamingRecordedSession replays typing while Claude Code is
// thinking (spinner) and streaming its answer above the composer.
func TestClaudeStreamingRecordedSession(t *testing.T) {
	res := replayRecorded(t, "claude-streaming-100x30", 30, 100, func() Profile { return newClaudeProfile() }, false)
	if res.stats.Diverged != 0 {
		t.Fatalf("%d rollbacks", res.stats.Diverged)
	}
}

// TestCodexStreamingRecordedSession replays a Codex session: the starfield
// on the empty composer, then typing while Codex works and streams its answer
// (the bottom pane shows a working status and the queue hint).
func TestCodexStreamingRecordedSession(t *testing.T) {
	res := replayRecorded(t, "codex-streaming-100x30", 30, 100, func() Profile { return newCodexProfile() }, false)
	if res.stats.Diverged != 0 {
		t.Fatalf("%d rollbacks", res.stats.Diverged)
	}
}

// TestGrokRecordedSession replays a real Grok Build session: typing that wraps
// twice (including a space hidden after an exactly full row), Shift+Enter,
// Backspace, Left with insertion, an inline paste, and a 6-line paste that
// becomes a chip (not predicted).
func TestGrokRecordedSession(t *testing.T) {
	res := replayRecorded(t, "grok-100x30", 30, 100, func() Profile { return newGrokProfile() }, false)
	if res.stats.Diverged != 0 || res.predicted < res.keys/2 {
		t.Fatalf("predicted %d of %d, %d rollbacks", res.predicted, res.keys, res.stats.Diverged)
	}
}

// TestOpencodeRecordedSessions replays real OpenCode 2.3.0 sessions: the home
// screen (a centered block that grows alternately up and down) and a session
// view (bottom-anchored composer), each with typing that wraps twice,
// Shift+Enter, Backspace, Left with insertion, an inline paste and a 6-line
// paste that becomes a chip.
func TestOpencodeRecordedSessions(t *testing.T) {
	for _, name := range []string{"opencode-100x30", "opencode-session-100x30"} {
		t.Run(name, func(t *testing.T) {
			res := replayRecorded(t, name, 30, 100, func() Profile { return newOpencodeProfile() }, false)
			if res.stats.Diverged != 0 || res.predicted < res.keys/2 {
				t.Fatalf("predicted %d of %d, %d rollbacks", res.predicted, res.keys, res.stats.Diverged)
			}
		})
	}
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

// TestOpencodeStreamingRecordedSession replays typing while OpenCode streams
// a long answer above the composer (with the cloud's real acknowledgements).
func TestOpencodeStreamingRecordedSession(t *testing.T) {
	res := replayRecorded(t, "opencode-streaming-100x30", 30, 100, func() Profile { return newOpencodeProfile() }, false)
	if res.stats.Diverged != 0 {
		t.Fatalf("%d rollbacks", res.stats.Diverged)
	}
}

// TestEditingKeysRecordedSessions replays the readline-style editing keys
// recorded on OpenCode 2.3.0 and Grok Build 1.0.77 (rec scenario "edit"):
// Ctrl+A/E, Home/End, Ctrl+W, Alt+Backspace, Ctrl+B/F, Up/Down, Alt+Left/
// Right, Esc b/f, Ctrl+K/U, Ctrl+H and Delete, on two logical lines and on a
// wrapped line. Every one of them must be predicted except Grok's Ctrl+B/F
// (they do not move its cursor) and Up/Down at the draft's first or last row.
func TestEditingKeysRecordedSessions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile func() Profile
	}{
		{"opencode-edit-100x30", func() Profile { return newOpencodeProfile() }},
		{"grok-edit-100x30", func() Profile { return newGrokProfile() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := replayRecorded(t, tc.name, 30, 100, tc.profile, false)
			if res.stats.Diverged != 0 {
				t.Fatalf("%d rollbacks", res.stats.Diverged)
			}
			t.Logf("barrier keys %v", res.stats.BarrierKeys)
			for key, n := range res.stats.BarrierKeys {
				if key != "ctrl+b" && key != "ctrl+f" && key != "esc" {
					t.Errorf("%d %s keys waited for the agent", n, key)
				}
			}
		})
	}
}

// TestMenuRecordedSessions replays the "menu" scenario: "/" and "@" popups
// (typing, Backspace, Esc), Tab and history Up/Down on an empty draft. In
// OpenCode the popup covers the rows above the box (and the home logo), so
// typing into it is predicted; Grok draws the rest of the best slash command
// inline after the cursor, so a slash command waits for the agent.
func TestMenuRecordedSessions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile func() Profile
		allowed map[string]bool // keys that may wait for the agent
	}{
		{"opencode-menu-100x30", func() Profile { return newOpencodeProfile() }, map[string]bool{"esc": true, "up": true}},
		{"grok-menu-100x30", func() Profile { return newGrokProfile() }, nil},
		{"claude-menu-100x30", func() Profile { return newClaudeProfile() }, map[string]bool{"esc": true, "up": true, "down": true}},
		// Cut before "@x": the popup then lists the recording user's other tasks.
		{"codex-menu-100x30", func() Profile { return newCodexProfile() }, map[string]bool{"esc": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := replayRecorded(t, tc.name, 30, 100, tc.profile, false)
			if res.stats.Diverged != 0 {
				t.Fatalf("%d rollbacks", res.stats.Diverged)
			}
			for key, n := range res.stats.BarrierKeys {
				if tc.allowed != nil && !tc.allowed[key] {
					t.Errorf("%d %s keys waited for the agent", n, key)
				}
			}
		})
	}
}

// TestPasteRecordedSessions replays bracketed pastes of 2 to 10 lines (with
// and without a trailing line break, a blank line inside), single lines of
// 200 and 800 characters, and a paste between typed words followed by
// Backspace into the chip. The recorder sent each paste in three reads, so
// this also covers a paste split across reads. OpenCode predicts all of them
// (its "[Pasted ~N lines] " chip from three lines or 150 characters); Grok's
// chip (from four lines) comes with a preview popup and is not predicted.
func TestPasteRecordedSessions(t *testing.T) {
	res := replayRecorded(t, "opencode-paste-100x30", 30, 100, func() Profile { return newOpencodeProfile() }, false)
	if res.stats.Diverged != 0 || len(res.stats.BarrierKeys) != 0 {
		t.Fatalf("%d rollbacks, waited: %v", res.stats.Diverged, res.stats.BarrierKeys)
	}
	res = replayRecorded(t, "grok-paste-100x30", 30, 100, func() Profile { return newGrokProfile() }, false)
	if res.stats.Diverged != 0 {
		t.Fatalf("grok: %d rollbacks", res.stats.Diverged)
	}
}
