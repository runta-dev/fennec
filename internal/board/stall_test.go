package board

import (
	"errors"
	"testing"
)

func runningRequest(t *testing.T) (Store, Project, Request) {
	t.Helper()
	store := Store{Home: t.TempDir()}
	p, err := store.CreateProject(Project{Name: "Stall", Queue: 1})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := store.CreateRequest(p.ID, "me", "Tag threads", "", "")
	if r, err = store.Claim(p.ID, r.Number, "calm-fox", "claude"); err != nil {
		t.Fatal(err)
	}
	if r, err = store.Reconcile(p.ID, r.Number, "calm-fox", "live"); err != nil || r.Status != Working || r.Runs[len(r.Runs)-1].State != "running" {
		t.Fatal(r.Status, err)
	}
	return store, p, r
}

func TestStalledRunLeavesRunningAndFreesItsSlot(t *testing.T) {
	store, p, r := runningRequest(t)
	if _, err := store.Stall(p.ID, r.Number, "other-run"); !errors.Is(err, ErrConflict) {
		t.Fatal("stalled a different run", err)
	}
	got, err := store.Stall(p.ID, r.Number, "calm-fox")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != NeedsYou || got.Failure != StalledFailure || !got.Stalled() || got.Runs[len(got.Runs)-1].Finished == nil {
		t.Fatalf("not stalled: %+v", got)
	}
	if _, err = store.Stall(p.ID, r.Number, "calm-fox"); !errors.Is(err, ErrConflict) {
		t.Fatal("stalled twice", err)
	}
	queues, err := store.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if q := queues[0]; q.Working != 0 || len(q.Active) != 0 || len(q.Stalled) != 1 || q.Stalled[0].Number != r.Number {
		t.Fatalf("stalled run still holds a slot: %+v", q)
	}
}

func TestLateReplyAfterStallIsKept(t *testing.T) {
	store, p, r := runningRequest(t)
	if _, err := store.Stall(p.ID, r.Number, "calm-fox"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Complete(p.ID, r.Number, "calm-fox", "claude", "Done after the build", Resolved)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Review || got.Failure != "" || got.Runs[len(got.Runs)-1].State != Resolved {
		t.Fatalf("late reply lost: %+v", got)
	}
}

func TestResumedAgentPutsStalledRunBackToRunning(t *testing.T) {
	store, p, r := runningRequest(t)
	if _, err := store.Stall(p.ID, r.Number, "calm-fox"); err != nil {
		t.Fatal(err)
	}
	got, err := store.ConfirmStarted(p.ID, r.Number, "calm-fox")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Working || got.Failure != "" || got.Runs[len(got.Runs)-1].State != "running" || got.Runs[len(got.Runs)-1].Finished != nil {
		t.Fatalf("not running again: %+v", got)
	}
}

func TestReplyToStalledRequestStartsAFreshRun(t *testing.T) {
	store, p, r := runningRequest(t)
	if _, err := store.Stall(p.ID, r.Number, "calm-fox"); err != nil {
		t.Fatal(err)
	}
	got, err := store.FollowUp(p.ID, r.Number, "me", "Please finish and report", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Queued || got.Failure != "" || got.Session != "" {
		t.Fatalf("reply did not requeue: %+v", got)
	}
	// The old run can no longer complete the request.
	if _, err = store.Complete(p.ID, r.Number, "calm-fox", "claude", "late", Resolved); !errors.Is(err, ErrConflict) {
		t.Fatal("old run completed a requeued request", err)
	}
}
