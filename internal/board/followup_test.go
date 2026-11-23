package board

import (
	"errors"
	"testing"
)

func TestFollowUpDuringExecutionKeepsSlotThenTakesPriority(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Replies", Queue: 1})
	a, _ := s.CreateRequest(p.ID, "me", "First", "", "")
	if _, err := s.Claim(p.ID, a.Number, "calm-fox", "claude"); err != nil {
		t.Fatal(err)
	}
	b, _ := s.CreateRequest(p.ID, "me", "Second", "", "")
	r, err := s.FollowUp(p.ID, a.Number, "me", "New information", nil)
	if err != nil || r.Status != Working {
		t.Fatal(r, err)
	}
	if _, err = s.Claim(p.ID, b.Number, "blue-bird", "claude"); !errors.Is(err, ErrConflict) {
		t.Fatal("follow-up released active slot", err)
	}
	r, err = s.Complete(p.ID, a.Number, "calm-fox", "claude", "Question", NeedsYou)
	if err != nil || r.Status != Queued {
		t.Fatal(r, err)
	}
	if _, err = s.Claim(p.ID, b.Number, "blue-bird", "claude"); !errors.Is(err, ErrConflict) {
		t.Fatal("new work jumped ahead", err)
	}
	r, err = s.Claim(p.ID, a.Number, "green-leaf", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindSession(p.ID, a.Number, r.Session, "calm-fox"); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Request(p.ID, a.Number)
	if r.ExecutionSession() != "calm-fox" || r.Session != "green-leaf" {
		t.Fatal("run and session conflated", r)
	}
	if _, err = s.Complete(p.ID, a.Number, "calm-fox", "claude", "Late answer", Resolved); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted stale completion", err)
	}
	r, err = s.Complete(p.ID, a.Number, "green-leaf", "claude", "Done", Resolved)
	if err != nil || r.Status != Resolved {
		t.Fatal("input scheduled twice", r, err)
	}
	if err = s.QueuePendingFollowUps(); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Request(p.ID, a.Number)
	if r.Status != Resolved {
		t.Fatal("requeued consumed follow-up")
	}
}

func TestFollowUpReplicatedAfterCompletionIsQueued(t *testing.T) {
	a := testStore(t)
	b := testStore(t)
	p, _ := a.CreateProject(Project{Name: "Replica", Queue: 1})
	r, _ := a.CreateRequest(p.ID, "me", "First", "", "")
	a.Claim(p.ID, r.Number, "calm-fox", "claude")
	sync := func() {
		x, err := a.Sync(Exchange{})
		if err != nil {
			t.Fatal(err)
		}
		y, err := b.Sync(x)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.Sync(y); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	if _, err := b.FollowUp(p.ID, r.Number, "me", "Concurrent reply", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Complete(p.ID, r.Number, "calm-fox", "claude", "Done", Resolved); err != nil {
		t.Fatal(err)
	}
	sync()
	if err := a.QueuePendingFollowUps(); err != nil {
		t.Fatal(err)
	}
	r, _ = a.Request(p.ID, r.Number)
	if r.Status != Queued {
		t.Fatal("lost concurrent user input", r)
	}
}
