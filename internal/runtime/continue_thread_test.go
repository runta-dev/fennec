package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fennec/internal/board"
)

// A reclaimed thread has no live terminal; resuming its conversation still
// continues it (THI-212). Launch itself never starts a blank agent for a
// missing conversation (see TestResumeWithoutMatchingHistoryDoesNotLaunchBlankConversation);
// ContinueThread then starts fresh and the run sends the full thread.
func TestContinueThreadReportsAResumedConversation(t *testing.T) {
	home := t.TempDir()
	store := board.Store{Home: home}
	store.SetRouting(board.Routing{Device: "1234abcd"})
	oldID, newID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	p, _ := store.CreateProject(board.Project{Name: "Thread", Queue: 1})
	request, _ := store.CreateRequest(p.ID, "me", "Question", "", "Ask")
	store.ClaimAt(p.ID, request.Number, oldID, "claude", "cloud")
	store.Complete(p.ID, request.Number, oldID, "claude", "Reply", board.NeedsYou)
	store.FollowUp(p.ID, request.Number, "me", "More", nil)
	if _, err := store.ClaimAt(p.ID, request.Number, newID, "claude", "cloud"); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".local/fennec/launches")
	os.MkdirAll(root, 0700)
	data, _ := json.Marshal(LocalLaunch{ID: oldID, Agent: "claude", Device: "1234abcd", LocalDirectory: "/"})
	os.WriteFile(filepath.Join(root, oldID+".json"), data, 0600)
	cwd := filepath.Join(home, ".fen", oldID)
	writeFixture(t, filepath.Join(cloudAgentHome(home, "claude"), "projects", ClaudeProjectDirectory(cwd), conversationA+".jsonl"), `{"type":"user","uuid":"u1","cwd":"`+cwd+`","message":"Ask"}`+"\n", time.Now().Add(-time.Hour))
	launched := false
	backend := &LocalRuntime{Home: home, Lookup: func(string) (string, error) { return "/bin/echo", nil }, Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ls" {
			return []byte(`{"terminals":[]}`), nil
		}
		launched = true
		return json.Marshal(LocalTerminal{Address: "t2", State: "live"})
	}}
	backend.Query = func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		if method == "terminal.list" {
			return json.RawMessage(`{"terminals":[]}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	terminal, continued, err := backend.ContinueThread(context.Background(), store, p.ID, request.Number)
	if err != nil {
		t.Fatal(err)
	}
	if !launched || !continued || terminal.Address != "t2" {
		t.Fatal(launched, continued, terminal)
	}
}

// FEN-11: the earlier cloud run never got a conversation (its input was
// refused), so there is nothing to resume. The follow-up starts a fresh agent
// that is given the whole thread, instead of failing for good.
func TestContinueThreadStartsFreshWhenNothingToResume(t *testing.T) {
	home := t.TempDir()
	store := board.Store{Home: home}
	store.SetRouting(board.Routing{Device: "1234abcd"})
	oldID, newID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	p, _ := store.CreateProject(board.Project{Name: "Thread", Queue: 1})
	request, _ := store.CreateRequest(p.ID, "me", "Question", "", "Ask")
	store.ClaimAt(p.ID, request.Number, oldID, "claude", "cloud")
	store.Complete(p.ID, request.Number, oldID, "claude", "Reply", board.NeedsYou)
	store.FollowUp(p.ID, request.Number, "me", "More", nil)
	if _, err := store.ClaimAt(p.ID, request.Number, newID, "claude", "cloud"); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".local/fennec/launches")
	os.MkdirAll(root, 0700)
	data, _ := json.Marshal(LocalLaunch{ID: oldID, Agent: "claude", Device: "1234abcd", LocalDirectory: "/"})
	os.WriteFile(filepath.Join(root, oldID+".json"), data, 0600)
	launches := 0
	backend := &LocalRuntime{Home: home, Lookup: func(string) (string, error) { return "/bin/echo", nil }, Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ls" {
			return []byte(`{"terminals":[]}`), nil
		}
		launches++
		return json.Marshal(LocalTerminal{Address: "t2", State: "live"})
	}}
	backend.Query = func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		if method == "terminal.list" {
			return json.RawMessage(`{"terminals":[]}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	terminal, continued, err := backend.ContinueThread(context.Background(), store, p.ID, request.Number)
	if err != nil {
		t.Fatal(err)
	}
	if launches != 1 || continued || terminal.Address != "t2" {
		t.Fatal(launches, continued, terminal)
	}
}
