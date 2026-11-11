package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAgentInventoryReusesBinariesWithoutExecutingThem(t *testing.T) {
	home := t.TempDir()
	managed := filepath.Join(home, ".local/fennec/agents/bin/codex")
	alias := filepath.Join(home, ".local/bin/codex")
	marker := filepath.Join(home, "executed")
	for _, dir := range []string{filepath.Dir(managed), filepath.Dir(alias)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	program := []byte("#!/bin/sh\nprintf called > " + shellQuote(marker) + "\n")
	if err := os.WriteFile(managed, program, 0700); err != nil {
		t.Fatal(err)
	}
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	if agentBinaryAvailable(home, "codex", missing) {
		t.Fatal("missing managed alias accepted")
	}
	if err := os.Symlink(managed, alias); err != nil {
		t.Fatal(err)
	}
	if !agentBinaryAvailable(home, "codex", missing) {
		t.Fatal("working managed binary not reused")
	}
	if err := os.Remove(managed); err != nil {
		t.Fatal(err)
	}
	if agentBinaryAvailable(home, "codex", missing) {
		t.Fatal("missing executable accepted")
	}
	global := filepath.Join(home, "global-codex")
	if err := os.WriteFile(global, program, 0700); err != nil {
		t.Fatal(err)
	}
	if !agentBinaryAvailable(home, "codex", func(string) (string, error) { return global, nil }) {
		t.Fatal("existing global binary not reused")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("inventory executed the binary")
	}
}
