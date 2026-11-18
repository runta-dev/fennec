package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// claudeStore lays out Claude Code's native install: versions/<v> binaries
// and ~/.local/bin/claude linked to one of them.
func claudeStore(t *testing.T, linked string, versions ...string) (entry, store, daemon string) {
	t.Helper()
	home := t.TempDir()
	store = filepath.Join(home, ".local/share/claude/versions")
	os.MkdirAll(store, 0755)
	for _, v := range versions {
		if err := os.WriteFile(filepath.Join(store, v), []byte("claude "+v), 0755); err != nil {
			t.Fatal(err)
		}
	}
	entry = filepath.Join(home, ".local/bin/claude")
	os.MkdirAll(filepath.Dir(entry), 0755)
	if err := os.Symlink(filepath.Join(store, linked), entry); err != nil {
		t.Fatal(err)
	}
	daemon = filepath.Join(home, "fen")
	os.WriteFile(daemon, []byte("#!/bin/sh\n"), 0755)
	return entry, store, daemon
}

// A self-updating entry is left to its updater.
func TestVersionStoreEntryIsNotWrapped(t *testing.T) {
	entry, store, daemon := claudeStore(t, "2.1.327", "2.1.318", "2.1.327")
	if err := InstallEntry(entry, daemon, "claude"); !errors.Is(err, ErrSelfUpdatingEntry) {
		t.Fatalf("install: %v", err)
	}
	if link, _ := os.Readlink(entry); link != filepath.Join(store, "2.1.327") {
		t.Fatalf("entry changed: %q", link)
	}
	if _, err := os.Lstat(entry + ".fennec-original.json"); !os.IsNotExist(err) {
		t.Fatal("record written")
	}
	if err := ReconcileEntry(entry, daemon, "claude"); !errors.Is(err, ErrSelfUpdatingEntry) {
		t.Fatalf("reconcile: %v", err)
	}
}

// An entry an earlier Fennec wrapped at 2.1.318, while the updater installed
// 2.1.327 beside it: removal and the next reconcile both restore the link at
// the newest version, not the stale one Claude Code complained about.
func TestWrappedVersionStoreEntryRestoresTheNewestVersion(t *testing.T) {
	for _, via := range []string{"uninstall", "reconcile"} {
		t.Run(via, func(t *testing.T) {
			entry, store, daemon := claudeStore(t, "2.1.318", "2.1.318")
			// as Fennec 0.5.5 wrapped it
			target := filepath.Join(store, "2.1.318")
			os.Remove(entry)
			os.Symlink(target, entry)
			if err := writeEntryRecord(entry, entryRecord{Link: target, Target: target, Wrapper: entryScript(daemon, "claude", target)}); err != nil {
				t.Fatal(err)
			}
			os.Remove(entry)
			os.WriteFile(entry, []byte(entryScript(daemon, "claude", target)), 0755)
			for _, v := range []string{"2.1.322", "2.1.327", "2.1.40"} {
				os.WriteFile(filepath.Join(store, v), []byte("claude "+v), 0755)
			}
			var err error
			if via == "uninstall" {
				err = UninstallEntry(entry)
			} else if err = ReconcileEntry(entry, daemon, "claude"); errors.Is(err, ErrSelfUpdatingEntry) {
				err = nil
			}
			if err != nil {
				t.Fatal(err)
			}
			if link, _ := os.Readlink(entry); link != filepath.Join(store, "2.1.327") {
				t.Fatalf("restored to %q", link)
			}
			if _, err := os.Lstat(entry + ".fennec-original.json"); !os.IsNotExist(err) {
				t.Fatal("record left behind")
			}
		})
	}
}

// Cursor's store keeps a directory per version; a relative link keeps its form.
func TestCurrentLinkKeepsTheLinkForm(t *testing.T) {
	home := t.TempDir()
	store := filepath.Join(home, ".local/share/cursor-agent/versions")
	for _, v := range []string{"2026.10.30-aaaaaaa", "2026.11.05-86047df"} {
		os.MkdirAll(filepath.Join(store, v), 0755)
		os.WriteFile(filepath.Join(store, v, "cursor-agent"), []byte(v), 0755)
	}
	target := filepath.Join(store, "2026.10.30-aaaaaaa/cursor-agent")
	if got := currentLink("../share/cursor-agent/versions/2026.10.30-aaaaaaa/cursor-agent", target); got != "../share/cursor-agent/versions/2026.11.05-86047df/cursor-agent" {
		t.Fatalf("relative: %q", got)
	}
	// npm and download links have no version store and are restored as recorded
	if got := currentLink("../lib/node_modules/@openai/codex/bin/codex.js", "/opt/homebrew/lib/node_modules/@openai/codex/bin/codex.js"); got != "../lib/node_modules/@openai/codex/bin/codex.js" {
		t.Fatalf("npm: %q", got)
	}
}
