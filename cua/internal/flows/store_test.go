package flows

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/models"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

func gitBinary() bool { _, err := exec.LookPath("git"); return err == nil }

func gitCmd(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return string(out), err
}

// decider answers a pick with fixed probabilities, choosing the likeliest.
type decider struct {
	probs map[string]float64
	asked []models.DecideArgs
}

func (d *decider) Decide(_ context.Context, a models.DecideArgs) (*models.Decided, error) {
	d.asked = append(d.asked, a)
	keys := make([]string, 0, len(d.probs))
	for k := range d.probs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return d.probs[keys[i]] > d.probs[keys[j]] })
	choice := "none"
	if len(keys) > 0 {
		choice = keys[0]
	}
	return &models.Decided{Answers: map[string]models.Answer{"pick": {Type: models.Choice, Choice: choice, Confidence: 1, Probabilities: d.probs}}, Served: protocol.Served{Role: models.RoleDecide, Model: "m"}}, nil
}

func TestFindThresholds(t *testing.T) {
	s, ck := noGit(t)
	if err := s.Save(&Saved{Name: "mail", Params: map[string]protocol.FlowParam{"to": {Example: "a@b.c"}, "subject": {}}, Steps: []protocol.Step{launch("Mail"), typ("{{to}}")}}, "seed"); err != nil {
		t.Fatal(err)
	}
	ck.add(time.Minute)
	if err := s.Save(&Saved{Name: "note", Params: map[string]protocol.FlowParam{"n": {Example: "1"}}, Steps: []protocol.Step{launch("Mousepad"), typ("replay run {{n}}")}}, "seed"); err != nil {
		t.Fatal(err)
	}
	// Newest first: f0 is note, f1 is mail.
	for _, tc := range []struct {
		probs map[string]float64
		flow  string
		p     float64
	}{
		{map[string]float64{"f1": 0.9, "f0": 0.05, "none": 0.05}, "mail", 0.9},
		{map[string]float64{"f0": 0.55, "none": 0.45}, "", 0.55},
		{map[string]float64{"f0": 0.62, "f1": 0.38}, "", 0.62},
		{map[string]float64{"none": 0.9, "f0": 0.1}, "", 0.9},
	} {
		d := &decider{probs: tc.probs}
		got, err := s.Find(context.Background(), "send a mail", d)
		if err != nil {
			t.Fatal(err)
		}
		name := ""
		if got.Flow != nil {
			name = got.Flow.Name
		}
		if name != tc.flow || got.P != tc.p {
			t.Errorf("%v: flow %q p %v, want %q %v", tc.probs, name, got.P, tc.flow, tc.p)
		}
		var q map[string]map[string]any
		_ = json.Unmarshal(d.asked[0].Questions, &q)
		crit := q["pick"]["criteria"].(map[string]any)
		if crit["f0"] != "note(n) once: type replay run {{n}}" || crit["f1"] != "mail(subject, to) once: type {{to}}" || crit["none"] == nil {
			t.Errorf("criteria = %v", crit)
		}
		if !strings.Contains(string(d.asked[0].State), "send a mail") {
			t.Errorf("state = %s", d.asked[0].State)
		}
	}
	empty, _ := noGit(t)
	if got, err := empty.Find(context.Background(), "x", nil); err != nil || got.Flow != nil {
		t.Errorf("find with no flows = %+v, %v", got, err)
	}
}

func TestIndexSkipsMalformed(t *testing.T) {
	s, _ := noGit(t)
	_ = s.ensure()
	_ = os.WriteFile(s.path("broken.json"), []byte(`{"name":"broken"}`), 0o600)
	good := `{"name":"good","params":{},"steps":[{"launch":"Notepad"},{"key":"ctrl+s"}],"idempotent":false,"saved_at":"2026-11-10T00:00:00.000Z"}`
	_ = os.WriteFile(s.path("good.json"), []byte(good), 0o600)
	if err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	index := fileText(t, s.path(IndexFile))
	if !regexp.MustCompile(`(?m)^good\(\) once: key ctrl\+s$`).MatchString(index) || strings.Contains(index, "broken") {
		t.Errorf("index:\n%s", index)
	}
	list, _ := s.List()
	if len(list) != 1 || list[0].Name != "good" || list[0].App != "Notepad" {
		t.Errorf("list = %+v", list)
	}
}

func TestCorruptFlowFile(t *testing.T) {
	s, ck := noGit(t)
	_ = s.ensure()
	for name, body := range map[string]string{
		"nosaved.json":   `{"name":"nosaved","params":{},"steps":[]}`,
		"noparams.json":  `{"name":"noparams","steps":[],"saved_at":"2026-01-01T00:00:00Z"}`,
		"nosteps.json":   `{"name":"nosteps","params":{},"saved_at":"2026-01-01T00:00:00Z"}`,
		"garbage.json":   `{not json`,
		"nullsteps.json": `{"name":"nullsteps","params":{},"steps":null,"saved_at":"2026-01-01T00:00:00Z"}`,
	} {
		_ = os.WriteFile(s.path(name), []byte(body), 0o600)
	}
	c := chainsOf(s, ck)
	if e := run(t, c, pass(launch("Notepad"), typ("x")), nil); e.SavedAs != "notepad" {
		t.Fatalf("a save beside malformed files = %+v", e)
	}
	index := fileText(t, s.path(IndexFile))
	if strings.TrimSpace(index) != "notepad(text) once: type {{text}}" {
		t.Errorf("index:\n%s", index)
	}
	if _, err := s.Load("nosaved"); err == nil {
		t.Error("loaded a malformed flow")
	}
}

func TestUnsignedCommit(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git does not run here")
	}
	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	cfg := "[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = " + filepath.Join(home, "no-such-gpg") + "\n[user]\n\tsigningkey = ABCDEF\n"
	if err := os.WriteFile(global, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	s, err := NewStore(filepath.Join(t.TempDir(), "flows"))
	if err != nil {
		t.Fatal(err)
	}
	ck := &clock{t: time.Now()}
	s.now = ck.now
	c := chainsOf(s, ck)
	if e := run(t, c, pass(launch("Notepad"), typ("x")), nil); e.SavedAs != "notepad" {
		t.Fatalf("saved %+v", e)
	}
	log := gitOut(t, s.Dir, "log", "--format=%s %G?")
	if !strings.Contains(log, "notepad: saved from 2 passing steps N") {
		t.Errorf("history:\n%s", log)
	}
	if ignored := fileText(t, s.path(".gitignore")); !strings.Contains(ignored, PendingFile) || !strings.Contains(ignored, RunLogFile) {
		t.Errorf(".gitignore = %q", ignored)
	}
	if tracked := gitOut(t, s.Dir, "ls-files"); strings.Contains(tracked, PendingFile) {
		t.Errorf("the chain record is in history: %s", tracked)
	}
}

func TestReplayAppTitleWait(t *testing.T) {
	ws := []view.RawWindow{
		{Key: "1", App: "TextEdit", Title: "a.txt", Active: true, PID: 5},
		{Key: "2", App: "TextEdit", Title: "report.rtf", PID: 5},
		{Key: "3", App: "Notes", Title: "report.rtf", PID: 6},
	}
	if w, ok := AppWindow(ws, "TextEdit", "report.rtf"); !ok || w.Key != "2" {
		t.Errorf("app window = %+v %v", w, ok)
	}
	if _, ok := AppWindow(ws, "TextEdit", "missing.txt"); ok {
		t.Error("an app step with a title took another window of the app")
	}
	if w, ok := AppWindow(ws, "TextEdit", ""); !ok || w.Key != "1" {
		t.Errorf("an app step without a title = %+v", w)
	}
}

func TestReplayAfterNewWindow(t *testing.T) {
	before := []view.RawWindow{
		{Key: "1", App: "Notes", Title: "Notes", PID: 5},
		{Key: "9", App: "Finder", Title: "Downloads", PID: 9, Active: true},
	}
	now := append(append([]view.RawWindow(nil), before...),
		view.RawWindow{Key: "2", App: "Notes", Title: "Export", PID: 5},
		view.RawWindow{Key: "3", App: "Notes", Title: "Export", PID: 5, OwnerKey: "1", Role: "sheet"},
		view.RawWindow{Key: "4", App: "Other", Title: "Export", PID: 8},
	)
	if w, ok := AfterWindow(before, now, "Export", 5, true); !ok || w.Key != "2" {
		t.Errorf("background after = %+v %v", w, ok)
	}
	if _, ok := AfterWindow(before, now[:2], "Export", 5, true); ok {
		t.Error("no new window counted")
	}
	// In the foreground a matching window behind the front one fails.
	if _, ok := AfterWindow(before, now, "Export", 5, false); ok {
		t.Error("a window open behind the front one passed")
	}
	front := []view.RawWindow{{Key: "2", Title: "Export", PID: 5, Active: true}}
	if _, ok := AfterWindow(before, front, "Export", 5, false); !ok {
		t.Error("the window that came to the front failed")
	}
}
