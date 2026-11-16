package predictstats

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"fennec/internal/predict"
)

func TestAppendSummarize(t *testing.T) {
	home := t.TempDir()
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	add := func(agent, version string, s predict.Stats) {
		t.Helper()
		s.Version = version
		if err := Append(home, FromStats(agent, 30, 100, true, false, s, at)); err != nil {
			t.Fatal(err)
		}
		at = at.Add(time.Minute)
	}
	add("codex", "0.169.0", predict.Stats{Keys: 100, Predicted: 90, Confirmed: 88, Diverged: 1, Barriers: 10, Locked: true,
		BarrierKeys: map[string]int{"up": 6, "tab": 4}, ConfirmP50: 240 * time.Millisecond})
	add("codex", "0.169.0", predict.Stats{Keys: 50, Predicted: 40, Confirmed: 40, Barriers: 10, Locked: true,
		BarrierKeys: map[string]int{"up": 10}, ConfirmP50: 260 * time.Millisecond})
	add("codex", "0.170.0", predict.Stats{Keys: 20, UnlockedKeys: 20})

	info, err := os.Stat(Path(home))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log %v, %v", info, err)
	}
	entries, err := Load(home)
	if err != nil || len(entries) != 3 {
		t.Fatalf("loaded %d entries, %v", len(entries), err)
	}
	summaries := Summarize(entries)
	if len(summaries) != 2 || summaries[0].Version != "0.170.0" || summaries[0].NeverLocked != 1 {
		t.Fatalf("summaries %+v", summaries)
	}
	old := summaries[1]
	if old.Sessions != 2 || old.Predicted != 130 || old.BarrierKeys["up"] != 16 || old.ConfirmP50MS != 260 {
		t.Fatalf("summary %+v", old)
	}
	var out bytes.Buffer
	if err := Write(&out, summaries); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"0.170.0", "1 (1 never locked)", "100.0%", "86.7%", "up 16, tab 4", "260 ms"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRotation(t *testing.T) {
	home := t.TempDir()
	if err := Append(home, Entry{Agent: "claude", Keys: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(Path(home), MaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if err := Append(home, Entry{Agent: "claude", Keys: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path(home) + ".1"); err != nil {
		t.Fatal("not rotated")
	}
	entries, _ := Load(home)
	if len(entries) != 2 || entries[1].Keys != 2 {
		t.Fatalf("entries %+v", entries)
	}
}

// TestTypedCausesAndFilter: typing excludes mouse and focus reports,
// rollback causes add up, and recording-tool sessions and old entries are
// filtered out.
func TestTypedCausesAndFilter(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		FromStats("codex", 37, 143, true, false, predict.Stats{Keys: 300, Typed: 100, Neutral: 200, Chars: 140, Commits: 12, Predicted: 90, Barriers: 10, Diverged: 3, Locked: true,
			RollbackCauses: map[string]int{"wrong:text:busy": 2, "missing:backspace": 1}}, at),
		FromStats("codex", 37, 143, true, false, predict.Stats{Keys: 50, Typed: 50, Predicted: 50, Diverged: 1, Locked: true,
			RollbackCauses: map[string]int{"wrong:text:busy": 1}}, at.Add(time.Hour)),
		FromStats("codex", 30, 100, true, true, predict.Stats{Keys: 200, Typed: 200, Predicted: 200, Locked: true}, at.Add(time.Hour)),
		{At: at.Add(-24 * time.Hour), Agent: "codex", Keys: 500, Predicted: 5, Locked: true}, // before typed was recorded
	}
	s := Summarize(Filter(entries, at, false))
	if len(s) != 1 || s[0].Sessions != 2 || s[0].Typed != 150 || s[0].Commits != 12 || s[0].RollbackCauses["wrong:text:busy"] != 3 {
		t.Fatalf("summary %+v", s)
	}
	if all := Summarize(Filter(entries, time.Time{}, true)); all[0].Sessions != 4 || all[0].Typed != 850 {
		t.Fatalf("all %+v", all)
	}
	var out bytes.Buffer
	if err := Write(&out, s); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"150", "12", "2.9%", "wrong:text:busy 3, missing:backspace 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, out.String())
		}
	}
}
