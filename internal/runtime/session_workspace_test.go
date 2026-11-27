package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionInstructionsPreserveUserFiles(t *testing.T) {
	home := t.TempDir()
	request := LocalLaunch{ID: "sunny-spicy", Agent: "codex", Device: "1234abcd", LocalDirectory: "/Users/example/repo"}
	cwd, err := prepareSessionWorkspace(home, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("user edits"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareSessionWorkspace(home, request); err == nil {
		t.Fatal("overwrote custom instructions")
	}
	data, _ := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if string(data) != "user edits" {
		t.Fatal("instructions lost")
	}
}
func TestSessionWorkspaceRejectsSymlink(t *testing.T) {
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, ".fen"), 0700)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(home, ".fen/sunny-spicy"))
	_, err := prepareSessionWorkspace(home, LocalLaunch{ID: "sunny-spicy", Agent: "codex", Device: "1234abcd", LocalDirectory: "/local"})
	if err == nil {
		t.Fatal("workspace symlink accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("wrote outside session directory")
	}
}
func TestClaudeImportsCanonicalSessionInstructions(t *testing.T) {
	home := t.TempDir()
	request := LocalLaunch{ID: "sunny-spicy", Agent: "claude", Device: "1234abcd", LocalDirectory: "/local"}
	cwd, err := prepareSessionWorkspace(home, request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(cwd, "CLAUDE.md"))
	if string(data) != "@AGENTS.md\n" {
		t.Fatal("divergent Claude prompt")
	}
	info, err := os.Stat(filepath.Join(cwd, "AGENTS.md"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("instruction permissions", err)
	}
	prompt, err := FennecPrompt(home, request)
	if err != nil || strings.Contains(prompt, "{{") || strings.Contains(prompt, ".fennec-local-context.json") {
		t.Fatal("incomplete prompt", err)
	}
}

func TestPromptUsesInstalledAlias(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh", "fennec")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1234abcd.conf"), []byte("Host Shiqis-MacBook-Pro.local.a3f fennec-device-1234abcd\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prompt, err := FennecPrompt(home, LocalLaunch{ID: "sunny-spicy", Agent: "codex", Device: "1234abcd", LocalDirectory: "/Users/example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Shiqis-MacBook-Pro.local.a3f", "context, not permission", "creates, edits, and deletions", "verify any sync required by the task"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

// FEN-179: only a chosen primary worker computer brings the section that has
// agents ask to move This Mac's access there.
func TestPromptPrimaryWorkerSection(t *testing.T) {
	home := t.TempDir()
	request := LocalLaunch{ID: "sunny-spicy", Agent: "codex", Device: "1234abcd", LocalDirectory: "/Users/example"}
	prompt, err := FennecPrompt(home, request)
	if err != nil || strings.Contains(prompt, "## Primary worker computer") || strings.Contains(prompt, "{{") {
		t.Fatal("primary section without a primary", err)
	}
	dir := filepath.Join(home, ".config", "fennec")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tailnet-computers.json"), []byte(`[{"id":"a","name":"jarvis","alias":"jarvis","host":"100.1.1.1","user":"u","added_at":1,"primary":true}]`), 0600); err != nil {
		t.Fatal(err)
	}
	prompt, err = FennecPrompt(home, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Primary worker computer", `chose "jarvis" as the primary`, "end to end while This Mac is asleep or offline", "explicitly agrees", "Primary worker computer for builds", "The project's rules still come first", "take precedence over this file"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(prompt, "{{") {
		t.Fatal("unfilled placeholder")
	}
}
