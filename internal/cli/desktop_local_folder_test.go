package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopLocalFolderTrustsClaudeSessionFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	dir, err := desktopLocalFolder(context.Background(), home, "claude", "desktop-123")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(home, ".fen", "desktop-sessions", "desktop-123") {
		t.Fatalf("folder %q", dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("folder not made: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil || !strings.Contains(string(b), `"hasTrustDialogAccepted": true`) || !strings.Contains(string(b), dir) {
		t.Fatalf("folder not trusted: %s %v", b, err)
	}
}

func TestDesktopLocalFolderLeavesOtherAgentsAndRejectsNames(t *testing.T) {
	home := t.TempDir()
	if dir, err := desktopLocalFolder(context.Background(), home, "codex", "desktop-1"); err != nil || dir != "" {
		t.Fatalf("codex: %q %v", dir, err)
	}
	for _, name := range []string{"", "../x", "desktop-1/../..", "other"} {
		if _, err := desktopLocalFolder(context.Background(), home, "claude", name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("profile written for rejected names")
	}
}
