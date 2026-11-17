package hookbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClaudeSettingsSurviveMacFileDeletionAndKeepSecretsOffArgv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cmux-claude-settings.fixture")
	raw := `{"env":{"EXAMPLE_SECRET":"private-value"},"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"known-cmux","timeout":5},{"type":"command","command":"user-audit","timeout":9}]}]},"extra":{"future":9007199254740993}}`
	os.WriteFile(path, []byte(raw), 0600)
	binding := Binding{ID: strings.Repeat("a", 64), Name: "cmux", Event: "SessionStart", Policy: Observer, TimeoutMS: 4000}
	args := []string{"--settings=" + path, "--", "--settings=literal prompt"}
	out, bindings, files, err := PrepareClaude(args, "calm-fox", func(e, c string) (*Binding, error) {
		if e == "SessionStart" && c == "known-cmux" {
			return &binding, nil
		}
		return nil, nil
	})
	if err != nil || len(bindings) != 1 || len(files) != 1 {
		t.Fatal(out, bindings, err)
	}
	os.Remove(path)
	if strings.Contains(strings.Join(out, " "), "private-value") || args[0] != "--settings="+path || out[len(out)-1] != "--settings=literal prompt" {
		t.Fatal("argv contents changed", out)
	}
	home := t.TempDir()
	remote, err := MaterializeClaudeSettings(home, "calm-fox", "claude", out, files)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(remote[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"private-value", "9007199254740993", "user-audit", "hook calm-fox " + binding.ID} {
		if !strings.Contains(string(data), want) {
			t.Fatal("lost settings", want)
		}
	}
	info, _ := os.Stat(remote[1])
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	again, err := MaterializeClaudeSettings(home, "calm-fox", "claude", out, files)
	if err != nil || !reflect.DeepEqual(remote, again) {
		t.Fatal("resume changed snapshot", err)
	}
	files[0].Data = json.RawMessage(`{}`)
	if _, err := MaterializeClaudeSettings(home, "calm-fox", "claude", out, files); err == nil {
		t.Fatal("overwrote prior snapshot")
	}
}
func TestClaudeSettingsValidationAndPromptBoundaries(t *testing.T) {
	for _, args := range [][]string{{"--", "--settings", "/missing"}, {"--system-prompt", "--settings=/missing"}} {
		out, _, files, err := PrepareClaude(args, "calm-fox", nil)
		if err != nil || len(files) != 0 || !reflect.DeepEqual(out, args) {
			t.Fatalf("prompt changed: %q %v", out, err)
		}
	}
	for _, body := range []string{"null", "[]", "broken", `{"x":"` + strings.Repeat("a", SettingsLimit) + `"}`} {
		path := filepath.Join(t.TempDir(), "settings")
		os.WriteFile(path, []byte(body), 0600)
		if _, _, _, err := PrepareClaude([]string{"--settings", path}, "calm-fox", nil); err == nil {
			t.Fatal("invalid file accepted")
		}
	}
	files := []ClaudeSettings{{Index: 1, Data: json.RawMessage(`{}`)}}
	if ValidateClaudeSettings("codex", []string{"--settings", SettingsPlaceholder}, files) == nil {
		t.Fatal("wrong agent accepted")
	}
	if ValidateClaudeSettings("claude", []string{"prompt", SettingsPlaceholder}, files) == nil {
		t.Fatal("wrong argument accepted")
	}
}
func TestClaudeSettingsInlineAndRepeated(t *testing.T) {
	args, _, files, err := PrepareClaude([]string{"--settings", `{"model":"one"}`, "--settings={\"model\":\"two\"}"}, "calm-fox", nil)
	if err != nil || len(files) != 2 || files[0].Index != 1 || files[1].Index != 3 {
		t.Fatal(args, files, err)
	}
}

func TestResumeImportReindexesSettings(t *testing.T) {
	files := []ClaudeSettings{{Index: 2, Data: json.RawMessage(`{}`)}}
	args := []string{"--resume", "conversation-id", "--settings", SettingsPlaceholder}
	got, err := ReindexClaudeSettings(args, files)
	if err != nil || got[0].Index != 3 || files[0].Index != 2 || ValidateClaudeSettings("claude", args, got) != nil {
		t.Fatal(got, err)
	}
}
