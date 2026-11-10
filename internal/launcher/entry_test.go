package launcher

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntryInstallRestoreAndExternalUpdate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	entry := filepath.Join(dir, "claude")
	daemon := filepath.Join(dir, "daemon")
	os.WriteFile(target, []byte("#!/bin/sh\necho original\n"), 0700)
	os.WriteFile(daemon, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700)
	os.Symlink("real", entry)
	if err := InstallEntry(entry, daemon, "claude"); err != nil {
		t.Fatal(err)
	}
	if err := InstallEntry(entry, daemon, "claude"); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(entry, "literal $(no)").Output()
	if err != nil || !strings.Contains(string(out), "literal $(no)") {
		t.Fatal(string(out), err)
	}
	if err := RestoreEntry(entry); err != nil {
		t.Fatal(err)
	}
	if link, _ := os.Readlink(entry); link != "real" {
		t.Fatal("original symlink not restored")
	}
	if err := InstallEntry(entry, daemon, "claude"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(entry, []byte("updated by vendor"), 0700)
	if err := RestoreEntry(entry); err == nil {
		t.Fatal("external update overwritten")
	}
}

func TestEntryResumesInterruptedPublication(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	entry := filepath.Join(dir, "claude")
	daemon := filepath.Join(dir, "daemon")
	os.WriteFile(target, []byte("#!/bin/sh\necho original\n"), 0700)
	os.WriteFile(daemon, []byte("#!/bin/sh\nexit 0\n"), 0700)
	os.Symlink("real", entry)
	if err := InstallEntry(entry, daemon, "claude"); err != nil {
		t.Fatal(err)
	}
	// Recreate the crash window: durable backup exists, original link remains.
	os.Remove(entry)
	os.Symlink("real", entry)
	if err := InstallEntry(entry, daemon, "claude"); err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(target)
	if original, err := OriginalEntry(entry); err != nil || original != canonical {
		t.Fatal(original, err)
	}
	if err := RestoreEntry(entry); err != nil {
		t.Fatal(err)
	}
}

func TestEntryDoesNotClaimUpdatedDaemonIsInstalled(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	entry := filepath.Join(dir, "claude")
	old := filepath.Join(dir, "old")
	next := filepath.Join(dir, "next")
	for _, p := range []string{target, old, next} {
		os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0700)
	}
	os.Symlink("real", entry)
	if err := InstallEntry(entry, old, "claude"); err != nil {
		t.Fatal(err)
	}
	if err := InstallEntry(entry, next, "claude"); err == nil {
		t.Fatal("stale daemon reported current")
	}
	if err := RestoreEntry(entry); err != nil {
		t.Fatal(err)
	}
	if err := InstallEntry(entry, next, "claude"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(entry)
	if !strings.Contains(string(data), next) {
		t.Fatal("new daemon missing")
	}
}

func TestEntryRepairPreservesOriginalAcrossInterruptedUpgrade(t *testing.T) {
	dir := t.TempDir()
	target, entry, old, next := filepath.Join(dir, "real"), filepath.Join(dir, "codex"), filepath.Join(dir, "old"), filepath.Join(dir, "next")
	for _, path := range []string{target, old, next} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("real", entry); err != nil {
		t.Fatal(err)
	}
	if err := InstallEntry(entry, old, "codex"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(entry + ".fennec-original.json")
	var record entryRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record.PendingWrapper = entryScript(next, "codex", target)
	if err := writeEntryRecord(entry, record); err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(target)
	if original, err := OriginalEntry(entry); err != nil || original != canonical {
		t.Fatal(original, err)
	}
	if err := os.WriteFile(entry, []byte(record.PendingWrapper), 0755); err != nil {
		t.Fatal(err)
	}
	if original, err := OriginalEntry(entry); err != nil || original != canonical {
		t.Fatal(original, err)
	}
	if err := ReconcileEntry(entry, next, "codex"); err != nil {
		t.Fatal(err)
	}
	if err := RestoreEntry(entry); err != nil {
		t.Fatal(err)
	}
	if link, _ := os.Readlink(entry); link != "real" {
		t.Fatal(link)
	}
}
func TestEntryRepairUpgradesOwnedWrapperButPreservesExternalEdit(t *testing.T) {
	dir := t.TempDir()
	target, entry, old, next := filepath.Join(dir, "real"), filepath.Join(dir, "codex"), filepath.Join(dir, "old"), filepath.Join(dir, "next")
	for _, path := range []string{target, old, next} {
		os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700)
	}
	os.Symlink("real", entry)
	if err := InstallEntry(entry, old, "codex"); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileEntry(entry, next, "codex"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(entry)
	if !strings.Contains(string(data), next) {
		t.Fatal("old executable retained")
	}
	os.WriteFile(entry, []byte("vendor update"), 0700)
	if err := ReconcileEntry(entry, old, "codex"); err == nil {
		t.Fatal("external edit overwritten")
	}
	data, _ = os.ReadFile(entry)
	if string(data) != "vendor update" {
		t.Fatal(string(data))
	}
}
