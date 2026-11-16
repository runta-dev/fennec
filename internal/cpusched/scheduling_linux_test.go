//go:build linux

package cpusched

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOnlyVerifiedControlProcessesAreElevated(t *testing.T) {
	home := t.TempDir()
	herdr := filepath.Join(home, ".local/bin/herdr-core")
	if err := os.MkdirAll(filepath.Dir(herdr), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(herdr, nil, 0700); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(home, ".fen/seal-holder")
	all := map[int]process{
		10: {10, 1, herdr, "serve", "1", "/"},
		11: {11, 10, "/agent/claude", "", "1", session},
		12: {12, 11, "/usr/bin/make", "", "1", "/tmp"},
		13: {13, 10, "/agent/codex", "", "1", filepath.Join(home, ".fen/drum-shadow")},
		14: {14, 10, "/bin/bash", "", "1", home},
		15: {15, 1, filepath.Join(home, ".local/fennec/node/bin/codex"), "app-server", "1", home},
		19: {19, 1, "/usr/sbin/sshd", "", "1", "/"},
		20: {20, 19, "/usr/sbin/sshd", "", "1", "/"},
		21: {21, 20, filepath.Join(home, ".local/fennec/terminal/build/fennec-terminal"), "serve", "1", "/"},
		22: {22, 21, herdr, "attach", "1", "/"},
		30: {30, 1, "/unrelated/herdr-core", "", "1", "/"},
		31: {31, 30, "/unrelated/claude", "", "1", session},
	}
	got := classify(home, all, 21)
	want := map[int]string{
		10: "control", 20: "control", 21: "control", 22: "control",
		// An agent and everything it starts, wherever they run, are the
		// session's.
		11: "workload/seal-holder", 12: "workload/seal-holder",
		13: "workload/drum-shadow",
		// Outside a session directory, or not started by Herdr: shared.
		14: sharedGroup, 15: sharedGroup,
		19: "", 30: "", 31: "",
	}
	for pid, group := range want {
		if got[pid] != group {
			t.Errorf("pid %d: %q, want %q", pid, got[pid], group)
		}
	}
}

func TestSessionGroups(t *testing.T) {
	for id, want := range map[string]string{
		"seal-holder":                      "workload/seal-holder",
		"0123456789abcdef0123456789abcdef": "workload/0123456789abcdef0123456789abcdef",
		"shared":                           "",
		"../control":                       "",
		"seal-holder/x":                    "",
		"":                                 "",
	} {
		if got := sessionGroup(id); got != want {
			t.Errorf("%q: %q, want %q", id, got, want)
		}
	}
	home := t.TempDir()
	if got := CommandFor(home, "seal-holder", []string{"claude"}); len(got) != 1 {
		t.Fatalf("without a resident binary the command is unchanged: %q", got)
	}
	binary := filepath.Join(home, ".local/fennec/bin/fennec-terminal")
	os.MkdirAll(filepath.Dir(binary), 0700)
	os.WriteFile(binary, nil, 0700)
	got := CommandFor(home, "seal-holder", []string{"claude", "--resume"})
	if want := []string{binary, "workload-exec", "--session", "seal-holder", "claude", "--resume"}; !equal(got, want) {
		t.Fatalf("%q", got)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestFocusWeights runs against a directory standing in for the cgroup tree:
// focus raises the session's weight once, input keeps it, and it returns
// after the hold.
func TestFocusWeights(t *testing.T) {
	root := t.TempDir()
	saved := groupRoot
	groupRoot = root
	defer func() { groupRoot = saved }()
	if err := os.MkdirAll(root+"/workload", 0755); err != nil {
		t.Fatal(err)
	}
	if err := ensureGroup("workload/seal-holder"); err != nil {
		t.Fatal(err)
	}
	weight := func() string {
		data, _ := os.ReadFile(root + "/workload/seal-holder/cpu.weight")
		return string(data)
	}
	if weight() != sessionWeight {
		t.Fatalf("new group weight %q", weight())
	}
	Focus("seal-holder")
	if weight() != focusWeight {
		t.Fatalf("focused weight %q", weight())
	}
	os.WriteFile(root+"/workload/seal-holder/cpu.weight", []byte("7"), 0644)
	Focus("seal-holder") // held: not written again
	if weight() != "7" {
		t.Fatal("focus rewrote a held weight")
	}
	Focus("drum-shadow") // no group: nothing to weight
	if _, err := os.Stat(root + "/workload/drum-shadow"); err == nil {
		t.Fatal("focus created a group")
	}
	focus.mu.Lock()
	focus.until["seal-holder"] = time.Now() // as if the hold had passed
	focus.mu.Unlock()
	deadline := time.Now().Add(focusHold + 5*time.Second)
	for weight() != sessionWeight && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if weight() != sessionWeight {
		t.Fatalf("weight %q after the hold", weight())
	}
}

// TestPruneKeepsSharedAndBusyGroups: only empty session groups not just
// placed are removed.
func TestPruneKeepsSharedAndBusyGroups(t *testing.T) {
	root := t.TempDir()
	saved := groupRoot
	groupRoot = root
	defer func() { groupRoot = saved }()
	for _, g := range []string{sharedGroup, "workload/seal-holder", "workload/drum-shadow", "workload/old-gone"} {
		if err := os.MkdirAll(root+"/"+g, 0755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(root+"/workload/drum-shadow/cgroup.procs", []byte("1\n"), 0644) // a plain directory is not empty
	pruneSessions(map[string]bool{"workload/seal-holder": true})
	for g, exists := range map[string]bool{sharedGroup: true, "workload/seal-holder": true, "workload/drum-shadow": true, "workload/old-gone": false} {
		if _, err := os.Stat(root + "/" + g); (err == nil) != exists {
			t.Errorf("%s exists: %v, want %v", g, err == nil, exists)
		}
	}
}

func TestTestsNeverUseHostGroups(t *testing.T) {
	if os.Getenv("FENNEC_CPU_GROUP_ROOT") == "" && groupRoot == "/sys/fs/cgroup/fennec" {
		t.Fatal("a test binary would reshape the host's CPU groups")
	}
}
