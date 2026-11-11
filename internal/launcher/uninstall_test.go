package launcher

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveShellBlockPreservesUnrelatedConfiguration(t *testing.T) {
	for _, original := range []string{"", "export EDITOR=vim", "# personal\nexport PATH=/custom:$PATH\n"} {
		home := t.TempDir()
		path := filepath.Join(home, ".zshrc")
		if err := os.WriteFile(path, []byte(original), 0640); err != nil {
			t.Fatal(err)
		}
		if err := appendShellSource(path, filepath.Join(home, ".local/share/fennec/shell-init.sh")); err != nil {
			t.Fatal(err)
		}
		if err := UninstallShellFile(path); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		if string(data) != original {
			t.Fatalf("personal configuration changed: %q", data)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0640 {
			t.Fatal("file permissions changed")
		}
	}
}
func TestRemoveShellBlockRejectsAmbiguousMarkers(t *testing.T) {
	for _, text := range []string{"# >>> Fennec CLI integration >>>\n", "# <<< Fennec CLI integration <<<\n# >>> Fennec CLI integration >>>\n", "# >>> Fennec CLI integration >>>\n# >>> Fennec CLI integration >>>\n# <<< Fennec CLI integration <<<\n"} {
		if _, err := RemoveShellBlock([]byte(text)); err == nil {
			t.Fatal("ambiguous block removed")
		}
	}
}
func TestUninstallRestoresManagedEntryButPreservesExternalUpdates(t *testing.T) {
	for _, updated := range []bool{false, true} {
		home := t.TempDir()
		target := filepath.Join(home, "agent")
		daemon := filepath.Join(home, "fen")
		entry := filepath.Join(home, "codex")
		for _, path := range []string{target, daemon} {
			if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(target, entry); err != nil {
			t.Fatal(err)
		}
		if err := InstallEntry(entry, daemon, "codex"); err != nil {
			t.Fatal(err)
		}
		replacement := []byte("#!/bin/sh\necho updated-agent\n")
		if updated {
			if err := os.WriteFile(entry, replacement, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := UninstallEntry(entry); err != nil {
			t.Fatal(err)
		}
		if updated {
			actual, _ := os.ReadFile(entry)
			if !bytes.Equal(actual, replacement) {
				t.Fatal("package manager update overwritten")
			}
		} else {
			link, err := os.Readlink(entry)
			if err != nil || link != target {
				t.Fatal("original not restored", err)
			}
		}
		for _, suffix := range []string{".fennec-original.json", ".fennec-lock"} {
			if _, err := os.Stat(entry + suffix); !os.IsNotExist(err) {
				t.Fatal("sidecar survived")
			}
		}
	}
}
func TestUninstallPreservesDotfilesSymlink(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "dotfiles.zshrc")
	entry := filepath.Join(home, ".zshrc")
	os.WriteFile(target, []byte("# mine\n"), 0600)
	os.Symlink(target, entry)
	if err := appendShellSource(entry, "/fixture/shell-init.sh"); err != nil {
		t.Fatal(err)
	}
	if err := UninstallShellFile(entry); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(entry); err != nil {
		t.Fatal("dotfiles symlink replaced")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "# mine\n" {
		t.Fatal("personal configuration changed")
	}
}
