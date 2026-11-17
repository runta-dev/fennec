package cmuxhooks

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeMatchesOnlyExactCommandsAndCorrectEvents(t *testing.T) {
	command := `"${CMUX_CLAUDE_HOOK_CMUX_BIN:-cmux}" hooks claude cron-create-guard`
	key, err := MatchClaude("PreToolUse", command)
	if err != nil || key != "PreToolUse/cron-create-guard" {
		t.Fatal(key, err)
	}
	for _, text := range []string{command + "; custom-command", strings.ReplaceAll(command, "cron-create-guard", "unknown")} {
		if _, err := MatchClaude("PreToolUse", text); err == nil {
			t.Fatal("modified hook accepted")
		}
	}
	if _, err := MatchClaude("SessionStart", command); err == nil {
		t.Fatal("wrong event accepted")
	}
	if key, err := MatchClaude("SessionStart", "user-audit"); err != nil || key != "" {
		t.Fatal(key, err)
	}
}
func TestClaudeAdapterRelaySanitizationAndOutput(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "cmux")
	script := `#!/bin/sh
case "$*" in *'hooks claude cron-create-guard --workspace '*) ;; *) exit 1;; esac
[ "$CMUX_AGENT_HOOK_RELAY_ORIGIN" = 1 ] || exit 1
[ -z "$CMUX_CLAUDE_PID$CMUX_AGENT_HOOK_INVOKER_PID$CMUX_CLAUDE_HOOK_SPOOL_DIR" ] || exit 1
body=$(cat)
case "$body" in *transcript_path*|*'"cwd"'*) exit 1;; esac
printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}'
printf 'decision reason' >&2
exit 2
`
	os.WriteFile(binary, []byte(script), 0700)
	local := &Local{Executable: binary, Workspace: "11111111-1111-1111-1111-111111111111", Surface: "22222222-2222-2222-2222-222222222222", Env: []string{"PATH=/usr/bin:/bin", "CMUX_CLAUDE_PID=10", "CMUX_AGENT_HOOK_INVOKER_PID=11", "CMUX_CLAUDE_HOOK_SPOOL_DIR=/private/spool"}}
	adapters, err := ClaudeAdapters(local)
	if err != nil {
		t.Fatal(err)
	}
	a := adapters["PreToolUse/cron-create-guard"]
	result := a.Execute(context.Background(), []byte(`{"session_id":"remote","cwd":"/cloud","transcript_path":"/cloud/private"}`))
	if result.Code != 2 || result.Status != "executed" || !json.Valid(result.Stdout) || string(result.Stderr) != "decision reason" {
		t.Fatal(result)
	}
	if a.Ref.ID == adapters["PreToolUse/pre-tool-use"].Ref.ID {
		t.Fatal("policy identity collision")
	}
}
func TestOfficialCmuxClaudeSettings(t *testing.T) {
	binary := os.Getenv("FENNEC_CMUX_BINARY")
	if binary == "" {
		t.Skip("official cmux binary required")
	}
	raw, err := exec.Command(binary, "hooks", "claude", "inject-settings").Output()
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct{ Type, Command string }
		}
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	count := 0
	for event, groups := range settings.Hooks {
		for _, group := range groups {
			for _, h := range group.Hooks {
				key, err := MatchClaude(event, h.Command)
				if err != nil || key == "" {
					t.Fatalf("unrecognized official %s hook: %v", event, err)
				}
				count++
			}
		}
	}
	if count < 12 {
		t.Fatal("incomplete official settings", count)
	}
	t.Logf("recognized all %d official Claude handlers", count)
}
