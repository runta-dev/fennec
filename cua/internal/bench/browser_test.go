package bench

import (
	"path/filepath"
	"strings"
	"testing"

	"fennec/cua/internal/bench/browserset"
)

// The browser set file lists every task of tasks.json once, with its
// apps and family, and every id loads.
func TestBrowserSetFile(t *testing.T) {
	l, err := ReadIDList(SetPath(filepath.Join("..", "..", "bench", "sets"), SetBrowser))
	if err != nil {
		t.Fatal(err)
	}
	if l.Name != SetBrowser || l.Sealed {
		t.Fatalf("name %q sealed %v", l.Name, l.Sealed)
	}
	defs, err := browserset.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(l.IDs) != len(defs) {
		t.Fatalf("%d ids, %d tasks", len(l.IDs), len(defs))
	}
	for i, d := range defs {
		id := l.IDs[i]
		if id != BrowserPrefix+d.ID {
			t.Errorf("line %d: %s, want %s%s (tasks.json order)", i+1, id, BrowserPrefix, d.ID)
		}
		if l.Apps[id] != BrowserApps(id) {
			t.Errorf("%s: apps %q, want %q", id, l.Apps[id], BrowserApps(id))
		}
		if !l.HasTag(id, "family") || l.Tags[id][0] != "family: "+d.Family {
			t.Errorf("%s: tags %v, want family: %s", id, l.Tags[id], d.Family)
		}
		task, err := LoadBrowserTask(id)
		if err != nil {
			t.Fatal(err)
		}
		if !IsBrowserTask(id) || task.ID != id || task.Pre != GuestPages+" setup "+d.ID ||
			len(task.Grading) != 1 || task.Grading[0].Command != GuestPages+" grade "+d.ID || task.Grading[0].Score != 100 ||
			task.AfterAgent == "" || task.Solve == "" || task.Install == nil || !task.ForceErrorFreePrep {
			t.Errorf("%s: %+v", id, task)
		}
		if !strings.Contains(task.Text, browserset.DefaultBase) && strings.Contains(d.Instruction, "{base}") {
			t.Errorf("%s: the text lacks the server URL", id)
		}
	}
	if _, err := LoadBrowserTask("browser/nope"); err == nil {
		t.Fatal("an unknown id loaded")
	}
	if IsBrowserTask("sys_apps/0cbfcae1") {
		t.Fatal("a macOSWorld id is a browser task")
	}
}

func TestBrowserBackendEnv(t *testing.T) {
	_, serve, err := BackendSetup(BackendFennec, ModelAccess{})
	if err != nil {
		t.Fatal(err)
	}
	cmd := BrowserBackendEnv + serve
	if !strings.HasPrefix(cmd, "export FENNEC_CUA_CHROMIUM=/Applications/Chromium.app ") ||
		!strings.Contains(cmd, `FENNEC_CUA_CHROMIUM_PROFILE="$HOME/Library/Application Support/Chromium"; export FENNEC_CUA_DIR=`) {
		t.Fatalf("%s", cmd)
	}
}
