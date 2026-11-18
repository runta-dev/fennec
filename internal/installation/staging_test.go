package installation

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func stagingDir(t *testing.T, parent, name, bundle string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	exe := filepath.Join(dir, bundle, "Contents/MacOS/fen")
	if err := os.MkdirAll(filepath.Dir(exe), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(exe, []byte("fen"), 0755)
	at := time.Now().Add(-age)
	for _, p := range []string{exe, filepath.Dir(exe), filepath.Join(dir, bundle, "Contents"), filepath.Join(dir, bundle), dir} {
		os.Chtimes(p, at, at)
	}
	return dir
}

// Previous bundles kept by updates are removed once nothing runs from them;
// one a process still executes from, and one being written now, stay.
func TestPruneStagingRemovesUnusedBackups(t *testing.T) {
	parent := t.TempDir()
	os.MkdirAll(filepath.Join(parent, "Fennec.app"), 0755)
	stale := stagingDir(t, parent, ".fennec-update-1", "Fennec.app", 48*time.Hour)
	interrupted := stagingDir(t, parent, ".fennec-update-2", "incoming.app", 72*time.Hour)
	used := stagingDir(t, parent, ".fennec-update-3", "Fennec.app", 48*time.Hour)
	fresh := stagingDir(t, parent, ".fennec-update-4", "incoming.app", time.Minute)
	old := RunningExecutables
	RunningExecutables = func() []string { return []string{filepath.Join(used, "Fennec.app/Contents/MacOS/fen"), "/usr/bin/zsh"} }
	defer func() { RunningExecutables = old }()

	removed := PruneStaging(parent)
	if len(removed) != 2 {
		t.Fatalf("removed %v", removed)
	}
	for dir, want := range map[string]bool{stale: false, interrupted: false, used: true, fresh: true} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s present=%v, want %v", filepath.Base(dir), err == nil, want)
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "Fennec.app")); err != nil {
		t.Fatal("the installed bundle was touched")
	}
}

// Only an incoming.app written just now is an update under way; one an
// interrupted install left days ago is not (it blocked every Trash cleanup).
func TestUpdateInProgressIgnoresAbandonedIncoming(t *testing.T) {
	parent := t.TempDir()
	stagingDir(t, parent, ".fennec-update-1", "incoming.app", 72*time.Hour)
	stagingDir(t, parent, ".fennec-update-2", "Fennec.app", time.Minute)
	if UpdateInProgress(parent) {
		t.Fatal("an abandoned incoming.app counted as an update under way")
	}
	stagingDir(t, parent, ".fennec-update-3", "incoming.app", time.Minute)
	if !UpdateInProgress(parent) {
		t.Fatal("an update under way was not seen")
	}
}
