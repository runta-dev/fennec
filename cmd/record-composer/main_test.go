package main

import (
	"os"
	"strings"
	"testing"

	"fennec/internal/predict"
)

// TestScenariosNeverSubmit: no scenario can send a prompt to any agent.
func TestScenariosNeverSubmit(t *testing.T) {
	for name, build := range scenarios {
		for _, agent := range []string{"claude", "codex", "opencode", "grok"} {
			if err := checkSafe(build(agent)); err != nil {
				t.Errorf("%s for %s: %v", name, agent, err)
			}
		}
	}
	if checkSafe([]step{key('\r')}) == nil || checkSafe([]step{typeText("a\nb")}) == nil {
		t.Fatal("Enter or a typed line break passed the check")
	}
}

func TestPrivateText(t *testing.T) {
	home, _ := os.UserHomeDir()
	trace := func(s string) predict.Trace { return predict.Trace{Output: []predict.TraceChunk{{Data: []byte(s)}}} }
	if got := privateText(trace("~/.fen/lime-bowl  ❯ hello ─────"), "lime-bowl", nil); len(got) != 0 {
		t.Fatalf("clean output flagged: %v", got)
	}
	got := privateText(trace("/root/.fen/other-task \u68c0\u67e5 "+home), "lime-bowl", nil) // a CJK task title
	want := []string{"another session (other-task)", "non-ASCII letters", "contains"}
	for _, w := range want {
		if !strings.Contains(strings.Join(got, "|"), w) {
			t.Errorf("findings %v lack %q", got, w)
		}
	}
}

// TestReplayMode replays a committed recording the way -replay does.
func TestReplayMode(t *testing.T) {
	r := run{agent: "opencode", scenario: "edit", dir: "../../internal/predict/testdata/traces/opencode-edit-100x30", cols: 100, rows: 30}
	r.check(config{rows: 30, cols: 100})
	if r.err != nil || r.report == nil || !r.report.OK() || !r.report.Stats.Locked {
		t.Fatalf("replay: %v %+v", r.err, r.report)
	}
}
