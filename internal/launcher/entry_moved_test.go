package launcher

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Kimi Code and OpenCode install a single regular executable, not a link.
// It is wrapped with its original kept beside it, the command never missing,
// and restored byte for byte (same inode) on removal.
func TestRegularExecutableEntryIsWrappedAndRestored(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "kimi")
	daemon := filepath.Join(dir, "fen")
	original := []byte("\x7fELF native kimi v1")
	for path, data := range map[string][]byte{entry: original, daemon: []byte("#!/bin/sh\n")} {
		if err := os.WriteFile(path, data, 0755); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.Stat(entry)
	if err := InstallEntry(entry, daemon, "kimi"); err != nil {
		t.Fatal(err)
	}
	wrapper, _ := os.ReadFile(entry)
	if !bytes.Contains(wrapper, []byte("# Fennec managed entry v1\n")) || !bytes.Contains(wrapper, []byte(MovedName(entry))) {
		t.Fatalf("entry not wrapped: %q", wrapper)
	}
	if got, err := OriginalEntry(entry); err != nil || got != MovedName(entry) {
		t.Fatalf("original %q %v", got, err)
	}
	if err := InstallEntry(entry, daemon, "kimi"); err != nil {
		t.Fatalf("second install not idempotent: %v", err)
	}
	if err := UninstallEntry(entry); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(entry)
	data, _ := os.ReadFile(entry)
	if err != nil || !bytes.Equal(data, original) || !os.SameFile(before, after) {
		t.Fatalf("not restored: %q", data)
	}
	for _, gone := range []string{MovedName(entry), entry + ".fennec-original.json"} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s left behind", gone)
		}
	}
}

// The agent's updater writes a new binary over the wrapper: reconcile drops
// the stale kept copy and wraps the new version; removal then restores it.
func TestRegularExecutableEntryFollowsSelfUpdate(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "opencode")
	daemon := filepath.Join(dir, "fen")
	os.WriteFile(daemon, []byte("#!/bin/sh\n"), 0755)
	os.WriteFile(entry, []byte("opencode v1"), 0755)
	if err := InstallEntry(entry, daemon, "opencode"); err != nil {
		t.Fatal(err)
	}
	// as an updater does: write a temp file and rename it over the entry
	tmp := filepath.Join(dir, ".opencode.new")
	os.WriteFile(tmp, []byte("opencode v2"), 0755)
	if err := os.Rename(tmp, entry); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileEntry(entry, daemon, "opencode"); err != nil {
		t.Fatal(err)
	}
	if kept, _ := os.ReadFile(MovedName(entry)); string(kept) != "opencode v2" {
		t.Fatalf("kept copy %q, want the new version", kept)
	}
	if err := UninstallEntry(entry); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(entry); string(data) != "opencode v2" {
		t.Fatalf("restored %q", data)
	}
	// an updater that replaced the wrapper before removal: its binary stays
	os.WriteFile(entry, []byte("opencode v2"), 0755)
	InstallEntry(entry, daemon, "opencode")
	os.WriteFile(entry, []byte("opencode v3"), 0755)
	if err := UninstallEntry(entry); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(entry); string(data) != "opencode v3" {
		t.Fatalf("updater's binary not kept: %q", data)
	}
	if _, err := os.Lstat(MovedName(entry)); !os.IsNotExist(err) {
		t.Fatal("stale kept copy left behind")
	}
}

// A file Fennec did not create at the kept name stops the install.
func TestRegularExecutableEntryPreservesForeignKeptName(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "kimi")
	daemon := filepath.Join(dir, "fen")
	os.WriteFile(daemon, []byte("#!/bin/sh\n"), 0755)
	os.WriteFile(entry, []byte("kimi"), 0755)
	os.WriteFile(MovedName(entry), []byte("someone else's"), 0600)
	if err := InstallEntry(entry, daemon, "kimi"); err == nil {
		t.Fatal("installed over a foreign file")
	}
	if data, _ := os.ReadFile(entry); string(data) != "kimi" {
		t.Fatal("entry changed")
	}
	if _, err := os.Lstat(entry + ".fennec-original.json"); !os.IsNotExist(err) {
		t.Fatal("record left behind")
	}
}
