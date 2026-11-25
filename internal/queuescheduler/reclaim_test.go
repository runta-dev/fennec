package queuescheduler

import (
	"context"
	"testing"

	"fennec/internal/board"
)

type endingExecutor struct {
	executorFixture
	ended []string
}

func (e *endingExecutor) End(_ context.Context, address string) error {
	e.ended = append(e.ended, address)
	for i := range e.sessions {
		if e.sessions[i].Address == address {
			e.sessions[i].State = "finished"
		}
	}
	return nil
}

// finishedRequest is a cloud request whose agent replied and that a person
// has not marked done yet; its session is live on terminal t1.
func finishedRequest(t *testing.T, status string) (board.Store, board.Project, board.Request, *endingExecutor) {
	t.Helper()
	store := board.Store{Home: t.TempDir()}
	p, err := store.CreateProject(board.Project{Name: "Reclaim", Queue: 2})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := store.CreateRequest(p.ID, "me", "Question", "", "")
	if r, err = store.ClaimAt(p.ID, r.Number, "calm-fox", "claude", "cloud"); err != nil {
		t.Fatal(err)
	}
	if r, err = store.Complete(p.ID, r.Number, "calm-fox", "claude", "Done", board.Resolved); err != nil {
		t.Fatal(err)
	}
	store.Role = "cloud"
	ex := &endingExecutor{executorFixture: executorFixture{sessions: []Session{{ID: "calm-fox", Address: "t1", State: "live", Status: status}}}}
	return store, p, r, ex
}

func reclaimStep(t *testing.T, s Scheduler) {
	t.Helper()
	listed, err := s.Executor.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.reclaim(context.Background(), listed)
}

func TestMarkDoneEndsTheThreadSession(t *testing.T) {
	store, p, r, ex := finishedRequest(t, "done")
	s := Scheduler{Store: store, Executor: ex}
	reclaimStep(t, s)
	if len(ex.ended) != 0 {
		t.Fatal("ended a session of a request awaiting review")
	}
	if _, err := store.Change(p.ID, r.Number, "", board.Resolved); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ex.ended) != 1 || ex.ended[0] != "t1" {
		t.Fatal("session not ended", ex.ended)
	}
	if got, _ := store.Request(p.ID, r.Number); got.Reclaim {
		t.Fatal("reclaim flag kept after the session ended")
	}
	reclaimStep(t, s)
	if len(ex.ended) != 1 {
		t.Fatal("ended twice", ex.ended)
	}
}

func TestMarkDoneWaitsForAWorkingAgent(t *testing.T) {
	store, p, r, ex := finishedRequest(t, "working")
	s := Scheduler{Store: store, Executor: ex}
	store.Change(p.ID, r.Number, "", board.Resolved)
	reclaimStep(t, s)
	if len(ex.ended) != 0 {
		t.Fatal("ended a working agent")
	}
	if got, _ := store.Request(p.ID, r.Number); !got.Reclaim {
		t.Fatal("gave up on a session that is still open")
	}
	ex.sessions[0].Status = "done"
	reclaimStep(t, s)
	if len(ex.ended) != 1 {
		t.Fatal("session not ended after the turn", ex.ended)
	}
}

func TestSessionsResolvedBeforeOrReopenedAreKept(t *testing.T) {
	store, p, r, ex := finishedRequest(t, "done")
	s := Scheduler{Store: store, Executor: ex}
	store.Change(p.ID, r.Number, "", board.Resolved)
	// Reopened before the scheduler got to it.
	if got, _ := store.Change(p.ID, r.Number, "", board.NeedsYou); got.Reclaim {
		t.Fatal("reopen kept the reclaim flag")
	}
	reclaimStep(t, s)
	// Marking an already-done request done again is not a new mark.
	store.Change(p.ID, r.Number, "", board.Resolved)
	store.Reclaimed(p.ID, r.Number)
	store.Change(p.ID, r.Number, "", board.Resolved)
	reclaimStep(t, s)
	if len(ex.ended) != 0 {
		t.Fatal("ended a session that was not newly marked done", ex.ended)
	}
}

func TestSessionSharedWithAnOpenRequestIsKept(t *testing.T) {
	store, p, r, ex := finishedRequest(t, "done")
	store.Role = ""
	other, _ := store.CreateRequest(p.ID, "me", "Other", "", "")
	if _, err := store.ClaimAt(p.ID, other.Number, "calm-fox", "claude", "cloud"); err != nil {
		t.Fatal(err)
	}
	store.Role = "cloud"
	store.Change(p.ID, r.Number, "", board.Resolved)
	reclaimStep(t, Scheduler{Store: store, Executor: ex})
	if len(ex.ended) != 0 {
		t.Fatal("ended a session an unfinished request uses")
	}
}
