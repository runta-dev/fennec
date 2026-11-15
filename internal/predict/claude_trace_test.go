package predict

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

// observed is one distinct composer state on a recorded screen.
type observed struct {
	at     float64
	lock   Lock
	screen *Screen
}

// composerStates replays a trace and records each distinct composer state at
// complete frames.
func composerStates(t *testing.T, name string, rows, cols int, profile Profile) []observed {
	t.Helper()
	s := NewScreen(rows, cols)
	var out []observed
	for _, c := range loadTrace(t, name) {
		s.Write(c.Data)
		if !s.Ground() {
			continue
		}
		lock, ok := profile.Locate(s)
		if !ok {
			continue
		}
		if n := len(out); n > 0 && out[n-1].lock.State.Equal(lock.State) && out[n-1].lock.Top == lock.Top {
			continue
		}
		out = append(out, observed{at: c.At, lock: lock, screen: s.Clone()})
	}
	return out
}

func describe(st State) string { return fmt.Sprintf("%q@%d", st.Text(), st.Cursor) }

func TestClaudeTracesParseComposer(t *testing.T) {
	cases := []struct {
		trace      string
		rows, cols int
		want       []string
	}{
		{"claude-100x30", 30, 100, []string{`""@0`, `"h"@1`, `"he"@2`, `"hel"@3`, `"hell"@4`, `"hello"@5`, `"hell"@4`, `"hell\n"@5`, `"hell\nx"@6`, `"hell\nx"@5`, `"hell\nZx"@6`, `""@0`}},
		{"claude-paste-80x24", 24, 80, []string{`""@0`, `"ab"@2`, `"abline one\nline two\nline three"@30`, `""@0`, `"[Pasted text #1 +59 lines]"@1`, `"[Pasted text #1 +59 lines]xy"@3`, `""@0`, `"[Image #2]"@1`, `"[Image #2] z"@3`, `""@0`}},
	}
	for _, tc := range cases {
		t.Run(tc.trace, func(t *testing.T) {
			var got []string
			for _, o := range composerStates(t, tc.trace, tc.rows, tc.cols, newClaudeProfile()) {
				got = append(got, describe(o.lock.State))
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("composer states\n got %v\nwant %v", got, tc.want)
			}
		})
	}
	// Word wrap: every intermediate state of a 45-character sentence typed
	// into a 40-column terminal parses, including the wrapped ones.
	states := composerStates(t, "claude-wrap-40x20", 20, 40, newClaudeProfile())
	last := states[len(states)-3].lock.State
	if describe(last) != `"the quick brown fox jumps over lazy dogs agai\n"@46` {
		t.Fatalf("wrapped composer parsed as %s", describe(last))
	}
}

// keyScript lists the keys sent while recording each trace, in order.
var keyScript = map[string][]string{
	"claude-100x30":      {"h", "e", "l", "l", "o", "\x7f", "\x1b[13;2u", "x", "\x1b[D", "Z", "\x03"},
	"claude-wrap-40x20":  append(strings.Split("the quick brown fox jumps over lazy dogs agai", ""), "\x1b[13;2u", "ab", "\x7f\x7f", "\x7f", "\x03"),
	"claude-paste-80x24": {"ab", "\x1b[200~line one\nline two\nline three\x1b[201~", "\x03", "\x1b[200~" + strings.Repeat("this is pasted line\n", 59) + "x\x1b[201~", "xy", "\x03"},
}

var traceSize = map[string][2]int{"claude-100x30": {30, 100}, "claude-wrap-40x20": {20, 40}, "claude-paste-80x24": {24, 80}}

// step is one recorded key: the composer state before it and the first
// recorded state that shows its full effect (a key written in one burst can
// render through intermediate frames).
type step struct {
	raw           string
	before, after observed
	predicted     State
	barrier       bool
	firstChange   float64 // when the composer first changed after the key
}

// walkKeys replays a trace's keys against its recorded composer states.
func walkKeys(t *testing.T, trace string) (Profile, []step) {
	t.Helper()
	size := traceSize[trace]
	states := composerStates(t, trace, size[0], size[1], newClaudeProfile())
	profile := newClaudeProfile()
	var steps []step
	i := 0
	for _, raw := range keyScript[trace] {
		if i+1 >= len(states) {
			break
		}
		before := states[i]
		profile.Locate(before.screen)
		st, barrier := before.lock.State, false
		for _, k := range ParseKeys([]byte(raw)) {
			edit := profile.Edit(k, st, EditContext{})
			if edit == nil {
				barrier = true
				break
			}
			st = edit(st)
		}
		next := i + 1
		if !barrier {
			for j := i + 1; j < len(states) && j <= i+4; j++ {
				if profile.Same(st, states[j].lock.State, states[j].screen) {
					next = j
					break
				}
			}
		}
		steps = append(steps, step{raw: raw, before: before, after: states[next], predicted: st, barrier: barrier, firstChange: states[i+1].at})
		i = next
	}
	return profile, steps
}

// TestClaudePredictionsMatchRecordedFrames walks each recorded keystroke:
// the profile's prediction for the key must render exactly the rows Claude
// Code drew in response.
func TestClaudePredictionsMatchRecordedFrames(t *testing.T) {
	for trace := range keyScript {
		t.Run(trace, func(t *testing.T) {
			profile, steps := walkKeys(t, trace)
			predicted, exact := 0, 0
			for _, sp := range steps {
				raw, before, after, st := sp.raw, sp.before, sp.after, sp.predicted
				if sp.barrier {
					continue
				}
				predicted++
				if !profile.Same(st, after.lock.State, after.screen) {
					t.Errorf("key %q: predicted %s, Claude showed %s", raw, describe(st), describe(after.lock.State))
					continue
				}
				profile.Locate(before.screen)
				paint, ok := profile.Render(before.lock, st, before.screen)
				if !ok {
					t.Errorf("key %q: prediction %s not renderable", raw, describe(st))
					continue
				}
				if diff := paintDiff(paint, after.screen); diff != "" {
					t.Errorf("key %q: painted rows differ from Claude's frame:\n%s", raw, diff)
					continue
				}
				exact++
			}
			if predicted == 0 || exact != predicted {
				t.Fatalf("%d of %d predicted keys rendered exactly", exact, predicted)
			}
		})
	}
}

// paintDiff compares painted rows (text) and the cursor with a screen.
func paintDiff(p Paint, s *Screen) string {
	var b strings.Builder
	for r, row := range p.Rows {
		var got strings.Builder
		for _, c := range row {
			if c.Width != 0 {
				got.WriteString(c.Glyph())
			}
		}
		want := s.RowText(r, 0, s.Cols)
		if strings.TrimRight(got.String(), " ") != strings.TrimRight(want, " ") {
			fmt.Fprintf(&b, "row %d\n paint %q\n frame %q\n", r, strings.TrimRight(got.String(), " "), strings.TrimRight(want, " "))
		}
	}
	if p.CursorRow != s.Row || p.CursorCol != s.Col {
		fmt.Fprintf(&b, "cursor painted at %d,%d, frame has %d,%d\n", p.CursorRow, p.CursorCol, s.Row, s.Col)
	}
	return b.String()
}

// TestEngineWithInjectedLatency replays a trace through the engine as if the
// link added 250 ms each way: keys are typed when the recording sent them and
// the server's frames arrive 500 ms later. Every predicted key must show the
// final rendering immediately, and the terminal must converge to the server
// screen with no leftover cells.
func TestEngineWithInjectedLatency(t *testing.T) {
	for _, trace := range []string{"claude-100x30", "claude-wrap-40x20", "claude-paste-80x24"} {
		t.Run(trace, func(t *testing.T) {
			size := traceSize[trace]
			chunks := loadTrace(t, trace)
			_, steps := walkKeys(t, trace)
			// A key was typed just before the first frame that changed after it.
			type typed struct {
				at  float64
				raw string
				ok  observed
			}
			var schedule []typed
			for _, sp := range steps {
				schedule = append(schedule, typed{at: sp.firstChange - 0.005, raw: sp.raw, ok: sp.after})
			}
			const rtt = 0.5
			now := time.Unix(0, 0)
			var term bytes.Buffer
			engine := New(&term, newClaudeProfile(), size[0], size[1], Options{noTick: true, Mode: ModeAlways, Now: func() time.Time { return now }})
			engine.trusted = true // the trace starts from a cleared screen
			defer engine.Close()
			at := func(sec float64) time.Time { return time.Unix(0, int64(sec*1e9)) }
			ci, immediate, predicted := 0, 0, 0
			for _, k := range schedule {
				for ci < len(chunks) && chunks[ci].At+rtt <= k.at {
					now = at(chunks[ci].At + rtt)
					engine.Server(chunks[ci].Data)
					ci++
				}
				now = at(k.at)
				engine.mu.Lock()
				locked := engine.locked
				engine.mu.Unlock()
				engine.Input([]byte(k.raw))
				if !locked {
					continue
				}
				engine.mu.Lock()
				shown := engine.display.Clone()
				hasPrediction := len(engine.painted) > 0
				engine.mu.Unlock()
				if !hasPrediction {
					continue
				}
				predicted++
				if composerRowsEqual(shown, k.ok.screen, k.ok.lock) {
					immediate++
				} else {
					t.Errorf("key %q at %.3f: shown\n%s\nwant\n%s", k.raw, k.at, dumpScreen(shown), dumpScreen(k.ok.screen))
				}
			}
			for ; ci < len(chunks); ci++ {
				now = at(chunks[ci].At + rtt)
				engine.Server(chunks[ci].Data)
			}
			now = now.Add(5 * time.Second)
			engine.Ack(1<<62, false)
			engine.mu.Lock()
			display, shadow := dumpScreen(engine.display), dumpScreen(engine.shadow)
			engine.mu.Unlock()
			if display != shadow {
				t.Fatalf("terminal did not converge:\n%s\nserver:\n%s", display, shadow)
			}
			if predicted == 0 || immediate != predicted {
				t.Fatalf("%d of %d predictions showed the final rendering immediately", immediate, predicted)
			}
			stats := engine.Stats()
			t.Logf("predicted=%d confirmed=%d diverged=%d expired=%d barriers=%d p50=%v", stats.Predicted, stats.Confirmed, stats.Diverged, stats.Expired, stats.Barriers, stats.ConfirmP50)
		})
	}
}

// composerRowsEqual compares the composer rows and cursor of two screens.
func composerRowsEqual(a, b *Screen, lock Lock) bool {
	for r := max(0, lock.Top-1); r <= min(a.Rows-1, lock.Bottom+1); r++ {
		if strings.TrimRight(a.RowText(r, 0, a.Cols), " ") != strings.TrimRight(b.RowText(r, 0, b.Cols), " ") {
			return false
		}
	}
	return a.Row == b.Row && a.Col == b.Col
}
