package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResumeLaunchUsesExistingConversationAndSharedLauncher(t *testing.T) {
	home := t.TempDir()
	oldID, newID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	previous := LocalLaunch{ID: oldID, Agent: "claude", Device: "1234abcd", LocalDirectory: "/"}
	root := filepath.Join(home, ".local/fennec/launches")
	os.MkdirAll(root, 0700)
	data, _ := json.Marshal(previous)
	os.WriteFile(filepath.Join(root, oldID+".json"), data, 0600)
	cwd := filepath.Join(home, ".fen", oldID)
	agentHome := cloudAgentHome(home, "claude")
	transcript := `{"type":"user","uuid":"u1","cwd":"` + cwd + `","message":"original question"}` + "\n"
	writeFixture(t, filepath.Join(agentHome, "projects", ClaudeProjectDirectory(cwd), conversationA+".jsonl"), transcript, time.Now().Add(-time.Hour))
	writeFixture(t, filepath.Join(agentHome, "projects", ClaudeProjectDirectory("/unrelated"), conversationB+".jsonl"), `{"cwd":"/unrelated"}`+"\n", time.Now())
	var command []string
	backend := &LocalRuntime{Home: home, Lookup: func(string) (string, error) { return "/bin/echo", nil }, Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ls" {
			return []byte(`{"terminals":[]}`), nil
		}
		command = args
		return json.Marshal(LocalTerminal{Address: "t2", State: "live"})
	}}
	_, err := backend.Launch(context.Background(), LocalLaunch{ID: newID, ResumeFrom: oldID, Agent: "claude", Device: "1234abcd", LocalDirectory: "/"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command, " ")
	if !strings.Contains(joined, "--resume "+conversationA) || strings.Contains(joined, conversationB) {
		t.Fatal("resumed an unrelated conversation", command)
	}
	if !strings.Contains(joined, "--cwd "+cwd) {
		t.Fatal("lost original workspace", command)
	}
}
func TestResumeWithoutMatchingHistoryDoesNotLaunchBlankConversation(t *testing.T) {
	home := t.TempDir()
	oldID := strings.Repeat("a", 32)
	root := filepath.Join(home, ".local/fennec/launches")
	os.MkdirAll(root, 0700)
	data, _ := json.Marshal(LocalLaunch{ID: oldID, Agent: "claude", Device: "1234abcd"})
	os.WriteFile(filepath.Join(root, oldID+".json"), data, 0600)
	backend := &LocalRuntime{Home: home, Lookup: func(string) (string, error) { return "/bin/echo", nil }, Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] != "ls" {
			t.Fatal("started blank agent")
		}
		return []byte(`{"terminals":[]}`), nil
	}}
	if _, err := backend.Launch(context.Background(), LocalLaunch{ID: strings.Repeat("b", 32), ResumeFrom: oldID, Agent: "claude", Device: "1234abcd"}); err == nil {
		t.Fatal("missing history accepted")
	}
}
