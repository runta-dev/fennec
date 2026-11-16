package predict

import (
	"io"
	"testing"
)

// TestStartupVersion reads each agent's version from its recorded startup
// banner, before any key is typed.
func TestStartupVersion(t *testing.T) {
	for name, want := range map[string]string{
		"claude-100x30":   "2.1.325",
		"codex-100x30":    "0.169.0",
		"grok-100x30":     "1.0.77",
		"opencode-100x30": "2.3.0",
	} {
		e := New(io.Discard, newClaudeProfile(), 30, 100, Options{noTick: true, Mode: ModeAlways})
		for _, c := range loadTrace(t, name) {
			e.Server(c.Data)
		}
		if got := e.Stats().Version; got != want {
			t.Errorf("%s: version %q, want %q", name, got, want)
		}
	}
}

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
