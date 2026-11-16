package predict

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type replayResult struct {
	keys, predicted, exact, paused int
	merged                         int // drawn by the agent only together with later keys
	stats                          Stats
}

// replayRecorded replays testdata/traces/<name> with Replay and fails the test
// on any prediction the agent never drew, any pause, a terminal that does not
// converge, or more than 5% merged predictions.
func replayRecorded(t *testing.T, name string, rows, cols int, profile func() Profile, fullScreen bool) replayResult {
	t.Helper()
	tr, err := LoadTrace(filepath.Join("testdata", "traces", name))
	if err != nil {
		t.Fatal(err)
	}
	rep := Replay(tr, rows, cols, profile, fullScreen)
	for _, p := range rep.Problems {
		t.Error(p)
	}
	for _, n := range rep.Notes {
		t.Log(n)
	}
	if !rep.Converged {
		t.Fatalf("terminal did not converge: %s", rep.Why)
	}
	res := replayResult{keys: rep.Keys, predicted: rep.Predicted, exact: rep.Exact, paused: rep.Paused, merged: rep.Merged, stats: rep.Stats}
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

// TestCJKRecordedSessions replays Chinese typed through an input method
// (phrases committed in one write) on all four agents at 80x24, 100x30 and
// 160x45: wide characters wrapping between CJK characters but not before
// closing punctuation, mixed ASCII words, Backspace and arrows over wide
// characters, Shift+Enter and a pasted Chinese sentence.
func TestCJKRecordedSessions(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "opencode", "grok"} {
		for _, size := range [][2]int{{80, 24}, {100, 30}, {160, 45}} {
			name := fmt.Sprintf("%s-cjk-%dx%d", agent, size[0], size[1])
			t.Run(name, func(t *testing.T) {
				res := replayRecorded(t, name, size[1], size[0], func() Profile { return ProfileFor(agent) }, false)
				if res.stats.Diverged != 0 {
					t.Fatalf("%d rollbacks", res.stats.Diverged)
				}
			})
		}
	}
}

// TestSizesRecordedSessions replays the typing and edit scenarios at 80x24
// and 160x45. Codex's are not included yet: the first keys, typed during its
// startup animation, are still mispredicted.
func TestSizesRecordedSessions(t *testing.T) {
	for _, agent := range []string{"claude", "opencode", "grok"} {
		for _, scenario := range []string{"typing", "edit"} {
			for _, size := range [][2]int{{80, 24}, {160, 45}} {
				name := fmt.Sprintf("%s-%s-%dx%d", agent, scenario, size[0], size[1])
				t.Run(name, func(t *testing.T) {
					res := replayRecorded(t, name, size[1], size[0], func() Profile { return ProfileFor(agent) }, false)
					if res.stats.Diverged != 0 {
						t.Fatalf("%d rollbacks", res.stats.Diverged)
					}
				})
			}
		}
	}
}

// TestPiRecordedSessions replays Pi 1.5.2 (launched with PI_HARDWARE_CURSOR=1)
// at 100x30: typing that wraps twice, Shift+Enter, Backspace, Left with
// insertion and an inline paste; the editing keys; pastes of 2 to 10 lines
// and long lines; and Chinese committed by an input method. Pi had no
// profile when recorded, so the cloud sent no acknowledgements; real
// sessions have them.
func TestPiRecordedSessions(t *testing.T) {
	for _, scenario := range []string{"typing", "edit", "paste", "cjk"} {
		name := "pi-" + scenario + "-100x30"
		t.Run(name, func(t *testing.T) {
			res := replayRecorded(t, name, 30, 100, func() Profile { return newPiProfile() }, false)
			if (scenario == "typing" || scenario == "edit") && res.stats.Diverged != 0 {
				t.Fatalf("%d rollbacks", res.stats.Diverged)
			}
			if res.predicted < res.keys/2 {
				t.Fatalf("only %d of %d keys predicted", res.predicted, res.keys)
			}
		})
	}
}

// TestKimiRecordedSessions replays Kimi Code 2.6.2 at 100x30 (the same
// scenarios as Pi). Kimi draws its own cursor (a reverse-video cell), so the
// predictions draw it too and keep the terminal cursor hidden. One
// prediction in the typing and one in the paste recording are drawn by Kimi
// only together with the next key in the middle of the draft, which the
// replay cannot tell from a misprediction; no rollback follows.
func TestKimiRecordedSessions(t *testing.T) {
	for _, scenario := range []string{"typing", "edit", "paste", "cjk"} {
		name := "kimi-" + scenario + "-100x30"
		t.Run(name, func(t *testing.T) {
			tr, err := LoadTrace(filepath.Join("testdata", "traces", name))
			if err != nil {
				t.Fatal(err)
			}
			rep := Replay(tr, 30, 100, func() Profile { return newKimiProfile() }, false)
			for _, p := range rep.Problems {
				t.Log(p)
			}
			t.Logf("keys=%d predicted=%d exact=%d rollbacks=%d", rep.Keys, rep.Predicted, rep.Exact, rep.Stats.Diverged)
			if !rep.Converged || !rep.Stats.Locked || len(rep.Problems) > 1 || rep.Predicted < rep.Keys/2 {
				t.Fatalf("converged=%v locked=%v problems=%d predicted %d of %d", rep.Converged, rep.Stats.Locked, len(rep.Problems), rep.Predicted, rep.Keys)
			}
			if (scenario == "typing" || scenario == "edit") && rep.Stats.Diverged != 0 {
				t.Fatalf("%d rollbacks", rep.Stats.Diverged)
			}
		})
	}
}

// barrierProfile leaves the editing keys unpredicted, so each is a barrier.
type barrierProfile struct{ Profile }

func (b barrierProfile) Edit(k Key, st State, ctx EditContext) EditFunc {
	switch k.Kind {
	case KeyCtrl, KeyHome, KeyEnd, KeyWordLeft, KeyWordRight, KeyAltBackspace, KeyUp, KeyDown:
		return nil
	}
	return b.Profile.Edit(k, st, ctx)
}

// TestBarrierKeysAppliedOnce replays Pi and Kimi sessions recorded with the
// cloud's acknowledgements (editing keys, and pastes cleared with Ctrl+U and
// Backspace) with every editing key unpredicted, with and without the
// acknowledgements. Keys typed behind such a key used to be applied a second
// time when the agent drew them in the frame that resolved it (without
// acknowledgements the barrier resolved only after a timeout).
func TestBarrierKeysAppliedOnce(t *testing.T) {
	for _, name := range []string{"pi-edit-acks-100x30", "pi-paste-acks-100x30", "kimi-edit-acks-100x30", "kimi-paste-acks-100x30"} {
		agent := strings.SplitN(name, "-", 2)[0]
		for _, acks := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/acks=%v", name, acks), func(t *testing.T) {
				tr, err := LoadTrace(filepath.Join("testdata", "traces", name))
				if err != nil {
					t.Fatal(err)
				}
				if !acks {
					tr.Acks = nil
				}
				rep := Replay(tr, 30, 100, func() Profile { return barrierProfile{ProfileFor(agent)} }, false)
				for _, p := range rep.Problems {
					t.Error(p)
				}
				if !rep.Converged || rep.Stats.Barriers < 20 {
					t.Fatalf("converged=%v barriers=%d", rep.Converged, rep.Stats.Barriers)
				}
				if acks && rep.Stats.Diverged != 0 {
					t.Fatalf("%d rollbacks", rep.Stats.Diverged)
				}
			})
		}
	}
}
