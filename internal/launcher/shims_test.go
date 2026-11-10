package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFenShimDispatchesResumeWithoutAgentInterception(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "daemon")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	shims := filepath.Join(directory, "shims")
	if err := InstallShims(shims, executable); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	output, err := exec.Command(filepath.Join(shims, "fen"), "resume", id).Output()
	if err != nil || string(output) != "fen\nresume\n"+id+"\n" {
		t.Fatal(string(output), err)
	}
}

func TestShimDoesNotOverwriteUserFile(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "daemon")
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0700)
	shims := filepath.Join(dir, "shims")
	os.Mkdir(shims, 0700)
	os.WriteFile(filepath.Join(shims, "codex"), []byte("user content"), 0700)
	if InstallShims(shims, exe) == nil {
		t.Fatal("user file replaced")
	}
	data, _ := os.ReadFile(filepath.Join(shims, "codex"))
	if string(data) != "user content" {
		t.Fatal("user content lost")
	}
}
func TestEnabledAgentsFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "takeover.json")
	value, err := EnabledAgents(path)
	if err != nil || len(value) != 0 {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(`{"agents":["codex"]}`), 0600)
	value, err = EnabledAgents(path)
	if err != nil || !value["codex"] {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(`invalid`), 0600)
	if _, err = EnabledAgents(path); err == nil {
		t.Fatal("invalid settings silently disabled takeover")
	}
}

func TestPublishEnabledUpdatesAndPreservesOnInvalidInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "takeover.json")
	if err := PublishEnabled(path, []string{"codex"}); err != nil {
		t.Fatal(err)
	}
	if err := PublishEnabled(path, []string{"unknown"}); err == nil {
		t.Fatal("invalid agent accepted")
	}
	got, err := EnabledAgents(path)
	if err != nil || !got["codex"] {
		t.Fatal("valid selection lost", err)
	}
	if err := PublishEnabled(path, nil); err != nil {
		t.Fatal(err)
	}
	got, err = EnabledAgents(path)
	if err != nil || len(got) != 0 {
		t.Fatal("selection not cleared", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("configuration permissions")
	}
}
