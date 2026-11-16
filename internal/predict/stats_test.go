package predict

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// TestKeyNames never reveal typed text beyond a menu trigger.
func TestKeyNames(t *testing.T) {
	for raw, want := range map[string]string{
		"hello": "text", "/mo": "text/", "@src": "text@", "\x0b": "ctrl+k",
		"\r": "enter", "\x1b[A": "up", "\t": "tab", "\x1b[200~secret\x1b[201~": "paste",
	} {
		keys := ParseKeys([]byte(raw))
		if len(keys) == 0 || keys[0].Name() != want {
			t.Errorf("%q: %v, want %s", raw, keys, want)
		}
	}
}

// TestBarrierKeysCounted: a key the profile cannot predict is counted by name.
func TestBarrierKeysCounted(t *testing.T) {
	res := replayRecorded(t, "grok-100x30", 30, 100, func() Profile { return newGrokProfile() }, false)
	total := 0
	for _, n := range res.stats.BarrierKeys {
		total += n
	}
	if !res.stats.Locked || total != res.stats.Barriers {
		t.Fatalf("locked=%v barrier keys %v sum to %d, barriers %d", res.stats.Locked, res.stats.BarrierKeys, total, res.stats.Barriers)
	}
	t.Logf("barrier keys %v, unlocked keys %d", res.stats.BarrierKeys, res.stats.UnlockedKeys)
}

// TestStartupVersion reads each agent's version from its own startup banner.
func TestStartupVersion(t *testing.T) {
	for name, want := range map[string]string{
		"claude-100x30":      "2.1.325",
		"codex-100x30":       "0.169.0",
		"grok-100x30":        "1.0.77",
		"opencode-100x30":    "2.3.0",
		"pi-typing-100x30":   "1.5.2",
		"kimi-typing-100x30": "2.6.2",
	} {
		agent := strings.SplitN(name, "-", 2)[0]
		e := New(io.Discard, ProfileFor(agent), 30, 100, Options{noTick: true, Mode: ModeAlways})
		for _, c := range loadTrace(t, name) {
			e.Server(c.Data)
		}
		if got := e.Stats().Version; got != want {
			t.Errorf("%s: version %q, want %q", name, got, want)
		}
	}
}

// TestVersionNotFromTranscript: attached to a running session, the banner is
// gone; version numbers in the conversation are not the agent's.
func TestVersionNotFromTranscript(t *testing.T) {
	e := New(io.Discard, newClaudeProfile(), 10, 80, Options{noTick: true, Mode: ModeAlways})
	e.Server([]byte("Pi v1.5.2 is installed\r\nFennec 0.3.11161301-dev\r\nOpenCode\r\n2.3.0\r\n"))
	if v := e.Stats().Version; v != "" {
		t.Fatalf("version %q from the transcript", v)
	}
}

// TestTypedAndNeutralCounted: mouse and focus reports are not typing; an
// input method's commit counts its characters.
func TestTypedAndNeutralCounted(t *testing.T) {
	e := New(io.Discard, newClaudeProfile(), 10, 80, Options{noTick: true, Mode: ModeAlways})
	e.Input([]byte("\x1b[<64;10;5M\x1b[<65;10;5M\x1b[I"))
	e.Input([]byte("hi"))
	e.Input([]byte("\u4e2d\u6587\u6d4b\u8bd5")) // an input method commit
	s := e.Stats()
	if s.Neutral != 3 || s.Typed != 2 || s.Chars != 6 || s.Commits != 1 {
		t.Fatalf("neutral=%d typed=%d chars=%d commits=%d", s.Neutral, s.Typed, s.Chars, s.Commits)
	}
}

// TestRollbackCausesAndCounts replays recordings where rollbacks happen (Pi
// pastes with editing keys unpredicted, without acknowledgements): every
// rollback has a cause, and a key predicted again after a rollback is not
// counted twice.
func TestRollbackCausesAndCounts(t *testing.T) {
	tr, err := LoadTrace(filepath.Join("testdata", "traces", "pi-paste-acks-100x30"))
	if err != nil {
		t.Fatal(err)
	}
	tr.Acks = nil
	rep := Replay(tr, 30, 100, func() Profile { return barrierProfile{newPiProfile()} }, false)
	s := rep.Stats
	causes := 0
	for _, n := range s.RollbackCauses {
		causes += n
	}
	t.Logf("rollbacks=%d causes=%v typed=%d predicted=%d barriers=%d", s.Diverged, s.RollbackCauses, s.Typed, s.Predicted, s.Barriers)
	if s.Diverged == 0 || causes != s.Diverged {
		t.Fatalf("%d rollbacks, causes %v", s.Diverged, s.RollbackCauses)
	}
	if s.Predicted+s.Barriers > s.Typed {
		t.Fatalf("predicted %d + barriers %d > typed %d", s.Predicted, s.Barriers, s.Typed)
	}
}
