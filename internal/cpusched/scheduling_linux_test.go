//go:build linux

package cpusched

import (
	"os"
	"path/filepath"
	"testing"
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
	all := map[int]process{
		10: {10, 1, herdr, "serve"},
		11: {11, 10, "/agent/claude", ""},
		12: {12, 11, "/usr/bin/make", ""},
		20: {20, 1, "/usr/sbin/sshd", ""},
		21: {21, 20, filepath.Join(home, ".local/fennec/terminal/build/fennec-terminal"), "serve"},
		22: {22, 21, herdr, "attach"},
		30: {30, 1, "/unrelated/herdr-core", ""},
		31: {31, 30, "/unrelated/claude", ""},
	}
	got := classify(home, all, 21)
	for _, pid := range []int{10, 20, 21, 22} {
		if got[pid] != "control" {
			t.Fatalf("control %d: %v", pid, got)
		}
	}
	for _, pid := range []int{11, 12} {
		if got[pid] != "workload" {
			t.Fatalf("workload %d: %v", pid, got)
		}
	}
	for _, pid := range []int{30, 31} {
		if got[pid] != "" {
			t.Fatalf("unrelated %d: %v", pid, got)
		}
	}
}
