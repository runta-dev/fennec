package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHideClaudeAttributionKeepsOtherSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := `{"model": "claude/x", "env": {"N": 12345678901234567890}, "attribution": {"commit": "Co-Authored-By: Claude", "extra": 1}}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := HideClaudeAttribution(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var got struct {
		Model       string         `json:"model"`
		Attribution map[string]any `json:"attribution"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "claude/x" || !strings.Contains(string(b), "12345678901234567890") || got.Attribution["commit"] != "" || got.Attribution["pr"] != "" || got.Attribution["extra"] != float64(1) {
		t.Fatalf("settings = %s", b)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Fatalf("mode changed to %v", info.Mode().Perm())
	}
	if err := HideClaudeAttribution(dir); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(b) {
		t.Fatal("hidden attribution rewrote the settings")
	}
}

func TestHideClaudeAttributionMissingAndBrokenSettings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "claude")
	if err := HideClaudeAttribution(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "settings.json")); !strings.Contains(string(b), `"commit": ""`) || !strings.Contains(string(b), `"pr": ""`) {
		t.Fatalf("missing settings not created: %s", b)
	}
	broken := `{"env": {`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := HideClaudeAttribution(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "settings.json")); string(b) != broken {
		t.Fatalf("half-written settings changed: %s", b)
	}
}
