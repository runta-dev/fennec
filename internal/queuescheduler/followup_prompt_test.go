package queuescheduler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"fennec/internal/board"
	r "fennec/internal/runtime"
)

// sentText runs CloudExecutor.Start against a herdr fake listing terminals and
// returns the text it typed.
func sentText(t *testing.T, store board.Store, p board.Project, request board.Request, terminals string) string {
	t.Helper()
	text := ""
	backend := &r.LocalRuntime{Home: store.Home, Run: func(context.Context, ...string) ([]byte, error) { t.Fatal("launched a new session"); return nil, nil }}
	backend.Query = func(_ context.Context, method string, args any) (json.RawMessage, error) {
		switch method {
		case "terminal.list":
			return json.RawMessage(terminals), nil
		case "terminal.label":
			return json.RawMessage(`{}`), nil
		case "terminal.send":
			text = args.(map[string]any)["text"].(string)
			return json.RawMessage(`{}`), nil
		}
		t.Fatal(method)
		return nil, nil
	}
	if err := (CloudExecutor{Runtime: backend, Store: store}).Start(context.Background(), p, request, board.RunPrompts(p, request, "fen")); err != nil {
		t.Fatal(err)
	}
	return text
}

func TestContinuedFollowUpSendsOnlyTheNewMessage(t *testing.T) {
	store := board.Store{Home: t.TempDir()}
	p, _ := store.CreateProject(board.Project{Name: "Thread", Queue: 1})
	request, _ := store.CreateRequest(p.ID, "me", "Question", "", "Original long request")
	store.ClaimAt(p.ID, request.Number, "calm-fox", "claude", "cloud")
	store.Complete(p.ID, request.Number, "calm-fox", "claude", "Long agent reply", board.NeedsYou)
	store.FollowUp(p.ID, request.Number, "me", "发新的 testflight", nil)
	request, err := store.ClaimAt(p.ID, request.Number, "green-leaf", "claude", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	text := sentText(t, store, p, request, `{"terminals":[{"address":"t1","state":"live","labels":{"fennec.session":"calm-fox","fennec.agent":"claude"}}]}`)
	if !strings.Contains(text, "发新的 testflight") || !strings.Contains(text, "green-leaf resolved|you|failed") {
		t.Fatalf("%q", text)
	}
	for _, old := range []string{"Original long request", "Long agent reply", "Queue knowledge"} {
		if strings.Contains(text, old) {
			t.Fatalf("resent %q: %q", old, text)
		}
	}
}

func TestOwnFreshTerminalGetsTheFullPrompt(t *testing.T) {
	store := board.Store{Home: t.TempDir()}
	p, _ := store.CreateProject(board.Project{Name: "Thread", Queue: 1})
	request, _ := store.CreateRequest(p.ID, "me", "Question", "", "Original long request")
	store.ClaimAt(p.ID, request.Number, "calm-fox", "claude", "cloud")
	store.Complete(p.ID, request.Number, "calm-fox", "claude", "Long agent reply", board.NeedsYou)
	store.FollowUp(p.ID, request.Number, "me", "More", nil)
	request, err := store.ClaimAt(p.ID, request.Number, "green-leaf", "claude", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	// Only this run's own terminal is live (launched fresh by an earlier
	// attempt of the same run): it holds none of the thread.
	text := sentText(t, store, p, request, `{"terminals":[{"address":"t2","state":"live","labels":{"fennec.session":"green-leaf","fennec.agent":"claude"}}]}`)
	if !strings.Contains(text, "Original long request") || !strings.Contains(text, "Queue knowledge") {
		t.Fatalf("%q", text)
	}
}
