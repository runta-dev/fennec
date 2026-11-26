package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// Cursor's `agent` dispatches as cursor-agent; a missing one is an error.
// (Another vendor's `agent` replaces the process, covered by the launcher tests.)
func TestCursorAliasCommand(t *testing.T) {
	home := t.TempDir()
	versions := filepath.Join(home, ".local/share/cursor-agent/versions/1")
	bin := filepath.Join(home, ".local/bin")
	os.MkdirAll(versions, 0700)
	os.MkdirAll(bin, 0700)
	os.WriteFile(filepath.Join(versions, "cursor-agent"), []byte("#!/bin/sh\nexit 0\n"), 0700)
	os.Symlink(filepath.Join(versions, "cursor-agent"), filepath.Join(bin, "agent"))
	t.Setenv("PATH", bin)
	if name, err := cursorAliasCommand(home, nil); err != nil || name != "cursor-agent" || agentForCommand(name) != "cursor" {
		t.Fatal(name, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := cursorAliasCommand(home, nil); err == nil {
		t.Fatal("dispatched a missing agent")
	}
}
