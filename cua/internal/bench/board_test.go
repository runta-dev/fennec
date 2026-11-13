package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewBoardKeepsOnlySelectedTasksAndNoTaskText(t *testing.T) {
	list := IDList{IDs: []string{"sys_apps/a1", "productivity/b2"}, Apps: map[string]string{"sys_apps/a1": "Calculator"}}
	t0 := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	scored := []TrialResult{
		{Task: "productivity/b2", Backend: BackendFennec, Trial: 2, Score: 100, Started: t0.Add(time.Minute),
			Grading:    []GradeOutput{{Command: "secret grading command", Score: 100, OK: true}},
			Transcript: &Transcript{Stop: "done", Turns: 3, ToolCalls: 2, Steps: []Step{{Tool: "observe"}, {Tool: "act", IsError: true}, {Text: "DONE"}}}},
		{Task: "sys_apps/a1", Backend: BackendDriver, Trial: 1, Score: 0, Started: t0},
		{Task: "other/zz", Backend: BackendDriver, Trial: 1},
	}
	harness := []TrialResult{{Task: "sys_apps/a1", Backend: BackendShuttle, Trial: 1, Attempt: 1, HarnessError: "boot"}}
	b := NewBoard(list, scored, harness, 0, "m")
	if len(b.Runs) != 2 || b.Runs[0].Task != "sys_apps/a1" || b.Trials != 2 {
		t.Fatalf("runs %+v trials %d", b.Runs, b.Trials)
	}
	r := b.Runs[1]
	if r.Tools["observe"] != 1 || r.Tools["act"] != 1 || r.ToolErrors != 1 || r.Turns != 3 || r.Stop != "done" {
		t.Errorf("run %+v", r)
	}
	if b.Tasks[0].App != "Calculator" || b.Tasks[1].Category != "productivity" || len(b.Harness) != 1 {
		t.Errorf("board %+v", b)
	}
	data, _ := json.Marshal(b)
	if strings.Contains(string(data), "secret grading command") {
		t.Error("the board carries a grading command")
	}
}

func TestLoadRunDetailAndShotPathRefuseEscapes(t *testing.T) {
	root := t.TempDir()
	dir := TrialDir(root, BackendFennec, "sys_apps/a1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := TrialResult{Task: "sys_apps/a1", Backend: BackendFennec, Trial: 1, Score: 100, Screenshot: "trial-1.jpg",
		Grading:    []GradeOutput{{Command: "grading command", Score: 100, OK: true, Output: "True"}},
		Transcript: &Transcript{Final: "DONE", Steps: []Step{{Tool: "observe", Result: strings.Repeat("x", 5000)}}}}
	if err := WriteResult(dir, TrialBase(1), r); err != nil {
		t.Fatal(err)
	}
	d, err := LoadRunDetail(root, BackendFennec, "sys_apps/a1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Final != "DONE" || len(d.Steps) != 1 || len(d.Steps[0].Result) > 1600 || d.Screenshot != "fennec-cua/sys_apps/a1/trial-1.jpg" {
		t.Errorf("detail %+v", d)
	}
	data, _ := json.Marshal(d)
	if strings.Contains(string(data), "grading command") {
		t.Error("the detail carries a grading command")
	}
	if _, err := LoadRunDetail(root, "../x", "sys_apps/a1", 1); err == nil {
		t.Error("a backend with .. was read")
	}
	if p, ok := ShotPath(root, d.Screenshot); !ok || p != filepath.Join(dir, "trial-1.jpg") {
		t.Errorf("shot %q %v", p, ok)
	}
	for _, bad := range []string{"../../etc/passwd", "fennec-cua/sys_apps/a1/trial-1.json", "fennec-cua/sys_apps/../a1/trial-1.jpg", "fennec-cua/sys_apps/a1/trial-1-harness-1.jpg"} {
		if _, ok := ShotPath(root, bad); ok {
			t.Errorf("ShotPath accepted %q", bad)
		}
	}
}
