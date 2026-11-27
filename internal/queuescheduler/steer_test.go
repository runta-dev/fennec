package queuescheduler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"fennec/internal/board"
)

type steerExecutor struct {
	executorFixture
	sent []string
}

func (e *steerExecutor) Query(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method == "terminal.send" {
		e.sent = append(e.sent, params.(map[string]any)["text"].(string))
	}
	return json.RawMessage(`{}`), nil
}

// FEN-169: replies posted while the agent works reach its session at once,
// once each, and the run's reply then answers them instead of queuing again.
func TestReplyWhileWorkingIsTypedIntoTheSession(t *testing.T) {
	store := board.Store{Home: t.TempDir(), Role: "local"}
	p, _ := store.CreateProject(board.Project{Name: "Thread", Queue: 1, Workers: []board.Worker{{Agent: "claude", On: true}}})
	request, _ := store.CreateRequest(p.ID, "me", "Question", "", "Start")
	e := &steerExecutor{}
	s := Scheduler{Store: store, Executor: e, Command: "fen"}
	step(t, s)
	request, _ = store.Request(p.ID, request.Number)
	if request.Status != board.Working {
		t.Fatal(request.Status)
	}
	e.sessions = []Session{{ID: request.Session, State: "live", Status: "working", Address: "t1"}}
	store.FollowUp(p.ID, request.Number, "me", "How is it going?", nil)
	step(t, s)
	step(t, s)
	if len(e.sent) != 1 || !strings.Contains(e.sent[0], "How is it going?") || !strings.Contains(e.sent[0], request.Session+" resolved|you|failed") {
		t.Fatalf("%q", e.sent)
	}
	if e.starts != 1 {
		t.Fatal("started another run", e.starts)
	}
	done, err := store.Complete(p.ID, request.Number, request.Session, "claude", "Done, and going well", board.Resolved)
	if err != nil || done.Status != board.Review {
		t.Fatal(done.Status, err)
	}
}
