package runtime

import (
	"fennec/internal/setup"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionStorageMatchesNativeLaunchAccount(t *testing.T) {
	for agent, variable := range map[string]string{"claude": "CLAUDE_CONFIG_DIR", "codex": "CODEX_HOME", "pi": "PI_CODING_AGENT_DIR", "kimi": "KIMI_CODE_HOME", "opencode": "XDG_DATA_HOME", "cursor": "CURSOR_CONFIG_DIR", "grok": "GROK_HOME"} {
		for _, selected := range []bool{false, true} {
			t.Run(agent+map[bool]string{false: "/migrated", true: "/cloud-login"}[selected], func(t *testing.T) {
				if selected && agent == "opencode" {
					t.Skip("OpenCode does not offer separate cloud login")
				}
				home := t.TempDir()
				base := filepath.Join(home, ".local/fennec/native")
				root := base
				if err := setup.ConfigureAgentMode(home, agent != "cursor" && agent != "grok"); err != nil {
					t.Fatal(err)
				}
				if selected {
					root = filepath.Join(home, ".local/fennec/native-logins", launchID)
					writeFixture(t, filepath.Join(base, ".cloud-logins.json"), string(encodeAny(map[string]string{agent: launchID})), time.Time{})
					writeFixture(t, filepath.Join(root, "login.json"), string(encodeAny(map[string]string{"id": launchID, "agent": agent})), time.Time{})
				}
				if err := os.MkdirAll(filepath.Join(root, agent), 0700); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, filepath.Join(root, "env.json"), "{}", time.Time{})
				env, err := setup.NativeAgentEnvironment(home, agent)
				if err != nil {
					t.Fatal(err)
				}
				expected := ""
				for _, entry := range env {
					if strings.HasPrefix(entry, variable+"=") {
						expected = strings.TrimPrefix(entry, variable+"=")
					}
				}
				if got := cloudAgentHome(home, agent); got == "" || got != expected {
					t.Fatalf("import root %q differs from native launch %q", got, expected)
				}
			})
		}
	}
}

func TestInvalidCloudAccountDoesNotPublishSessionElsewhere(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".local/fennec/native/.cloud-logins.json"), `{"grok":"missing-login"}`, time.Time{})
	b := SessionImport{Version: 1, Agent: "grok", Conversation: conversationA, LaunchID: launchID, Marker: "fixture", Files: grokFixture()}
	if _, err := ApplySessionImport(home, b); err == nil {
		t.Fatal("import ignored invalid account selection")
	}
	if HasCloudSession(home, "grok", conversationA, "fixture") {
		t.Fatal("invalid account reported as present")
	}
	if _, err := os.Stat(filepath.Join(home, ".local/fennec/native/grok")); !os.IsNotExist(err) {
		t.Fatal("created a different account's session directory")
	}
}
