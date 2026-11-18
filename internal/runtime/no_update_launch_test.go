package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	agents "fennec/internal/agentcmd"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fennec pins cloud agent versions, so every cloud session turns off the
// agent's own update check in both agent modes and both launch paths.
// OpenCode, which probes its executable, is covered by its own launch test.
func TestCloudLaunchDisablesAgentUpdateChecks(t *testing.T) {
	want := map[string]string{
		"claude": "DISABLE_AUTOUPDATER=1",
		"codex":  "check_for_update_on_startup=false",
		"pi":     "PI_SKIP_VERSION_CHECK=1",
		"kimi":   "KIMI_CODE_NO_AUTO_UPDATE=1",
		"grok":   "GROK_DISABLE_AUTOUPDATER=1",
		"cursor": "--disable-auto-update",
	}
	for agent, setting := range want {
		for _, native := range []bool{false, true} {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, ".local/fennec"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".local/fennec/agent-mode.json"), []byte(fmt.Sprintf(`{"native":%v}`, native)), 0600); err != nil {
				t.Fatal(err)
			}
			var command []string
			backend := &LocalRuntime{Home: home, Lookup: func(name string) (string, error) { return "/usr/bin/" + name, nil }, Run: func(ctx context.Context, args ...string) ([]byte, error) {
				if args[0] == "ls" {
					return []byte(`{"terminals":[]}`), nil
				}
				for i, arg := range args {
					if arg == "--" {
						command = append([]string{}, args[i+1:]...)
						break
					}
				}
				return json.Marshal(LocalTerminal{Address: "t1", State: "live"})
			}}
			request := LocalLaunch{ID: strings.Repeat("d", 32), Agent: agent, LocalDirectory: "/project", Device: "1234abcd", Arguments: []string{"--model", "m"}}
			if _, err := backend.Launch(context.Background(), request); err != nil {
				t.Fatal(agent, err)
			}
			joined := strings.Join(command, " ")
			if !strings.Contains(joined, setting) || !strings.HasSuffix(joined, "--model m") {
				t.Fatalf("%s native=%v command %q", agent, native, joined)
			}
		}
	}
	// The older version-checked script carries only the command-line form.
	payload := launchPayload(t, mustLaunchScript(t, "codex"))
	if !strings.Contains(strings.Join(payload.Command, " "), strings.Join(agents.NoUpdateArguments("codex"), " ")+" --model m") {
		t.Fatalf("codex script command %q", payload.Command)
	}
}

func mustLaunchScript(t *testing.T, agent string) string {
	t.Helper()
	script, err := LaunchScriptWithArgs(strings.Repeat("f", 32), agent, "/tmp", []string{"--model", "m"})
	if err != nil {
		t.Fatal(err)
	}
	return script
}

type launchScriptPayload struct {
	Command []string `json:"command"`
}

func launchPayload(t *testing.T, script string) launchScriptPayload {
	t.Helper()
	encoded := strings.Split(strings.Split(script, "python3 - '")[1], "'")[0]
	data, err := base64.StdEncoding.DecodeString(encoded)
	var payload launchScriptPayload
	if err != nil || json.Unmarshal(data, &payload) != nil {
		t.Fatal("invalid launch payload")
	}
	return payload
}
