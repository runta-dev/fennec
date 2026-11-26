package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustClaudeFoldersMarksQueueSessionAndKeepsTheRest(t *testing.T) {
	home := t.TempDir()
	profile := ClaudeProfile(home, filepath.Join(home, ".claude"))
	if profile != filepath.Join(home, ".claude.json") {
		t.Fatalf("default profile = %s", profile)
	}
	original := `{"numStartups": 12345678901234567890, "oauthAccount": {"email": "x"}, "projects": {"/other": {"hasTrustDialogAccepted": false, "allowedTools": ["Bash"]}}}`
	if err := os.WriteFile(profile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".fen/queue-sessions", "abc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := TrustClaudeFolders(profile, dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "12345678901234567890") {
		t.Fatalf("large number was rewritten: %s", b)
	}
	var got struct {
		OAuth    map[string]any            `json:"oauthAccount"`
		Projects map[string]map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.OAuth["email"] != "x" || got.Projects["/other"]["hasTrustDialogAccepted"] != false || got.Projects["/other"]["allowedTools"] == nil {
		t.Fatalf("other settings changed: %s", b)
	}
	real, _ := filepath.EvalSymlinks(dir)
	for _, k := range []string{dir, real} {
		if got.Projects[k]["hasTrustDialogAccepted"] != true {
			t.Fatalf("%s not trusted: %s", k, b)
		}
	}
	if info, _ := os.Stat(profile); info.Mode().Perm() != 0o644 {
		t.Fatalf("mode changed to %v", info.Mode().Perm())
	}
	before, _ := os.ReadFile(profile)
	if err := TrustClaudeFolders(profile, dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(profile)
	if string(before) != string(after) {
		t.Fatal("already trusted folder rewrote the profile")
	}
}

func TestTrustClaudeFoldersCustomConfigDirAndBrokenFile(t *testing.T) {
	home := t.TempDir()
	custom := filepath.Join(home, "cfg")
	profile := ClaudeProfile(home, custom)
	if profile != filepath.Join(custom, ".claude.json") {
		t.Fatalf("custom profile = %s", profile)
	}
	if err := TrustClaudeFolders(profile, home); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(profile); !strings.Contains(string(b), `"hasTrustDialogAccepted": true`) {
		t.Fatalf("missing profile not created: %s", b)
	}
	if err := os.WriteFile(profile, []byte(`{"projects": {`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := TrustClaudeFolders(profile, home); err == nil {
		t.Fatal("half-written profile was overwritten")
	}
	if b, _ := os.ReadFile(profile); string(b) != `{"projects": {` {
		t.Fatalf("half-written profile changed: %s", b)
	}
}

func TestPrepareClaudeLaunchFinishesOnboardingOnce(t *testing.T) {
	home := t.TempDir()
	profile := ClaudeProfile(home, "")
	if err := os.WriteFile(profile, []byte(`{"hasCompletedOnboarding": false, "theme": "light"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareClaudeLaunch(profile, home); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Onboarded bool                      `json:"hasCompletedOnboarding"`
		Theme     string                    `json:"theme"`
		Projects  map[string]map[string]any `json:"projects"`
	}
	b, _ := os.ReadFile(profile)
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Onboarded || got.Theme != "light" || got.Projects[home]["hasTrustDialogAccepted"] != true {
		t.Fatalf("profile not prepared: %s", b)
	}
	if err := PrepareClaudeLaunch(profile, home); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(profile); string(after) != string(b) {
		t.Fatal("prepared profile was rewritten")
	}
	if err := TrustClaudeFolders(ClaudeProfile(home, filepath.Join(home, "native")), home); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "native", ".claude.json")); strings.Contains(string(b), "hasCompletedOnboarding") {
		t.Fatalf("trust alone finished onboarding: %s", b)
	}
}
