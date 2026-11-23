package board

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestQueueOrderPauseAndSlots(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery", Queue: 1})
	for _, title := range []string{"First", "Second", "Third"} {
		if _, err := s.CreateRequest(p.ID, "me", title, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	s.Reply(p.ID, 1, "me", "", "An extra comment must not change FIFO")
	q, _ := s.Queue()
	if q[0].Queued[0].Number != 1 {
		t.Fatal(q)
	}
	if _, err := s.Move(p.ID, 3, 1); err != nil {
		t.Fatal(err)
	}
	s.UpdateProject(p.ID, json.RawMessage(`{"paused":true}`))
	if _, err := s.Claim(p.ID, 3, "calm-fox", "codex"); !errors.Is(err, ErrConflict) {
		t.Fatal("claimed paused queue", err)
	}
	s.UpdateProject(p.ID, json.RawMessage(`{"paused":false}`))
	if _, err := s.Claim(p.ID, 1, "calm-fox", "codex"); !errors.Is(err, ErrConflict) {
		t.Fatal("ignored order", err)
	}
	if _, err := s.Claim(p.ID, 3, "calm-fox", "codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(p.ID, 1, "blue-bird", "codex"); !errors.Is(err, ErrConflict) {
		t.Fatal("exceeded slots", err)
	}
	if _, err := s.Complete(p.ID, 3, "calm-fox", "codex", "Please check", NeedsYou); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(p.ID, 1, "blue-bird", "codex"); err != nil {
		t.Fatal("waiting user retained slot", err)
	}
	if _, err := s.Complete(p.ID, 1, "calm-fox", "codex", "Late result", Resolved); !errors.Is(err, ErrConflict) {
		t.Fatal("stale session accepted", err)
	}
	if _, err := s.Fail(p.ID, 1, "blue-bird"); err != nil {
		t.Fatal(err)
	}
	q, _ = s.Queue()
	if q[0].Working != 1 || len(q[0].Queued) != 1 {
		t.Fatal(q)
	}
	if _, err := s.Continue(p.ID, 1, "me", ""); !errors.Is(err, ErrConflict) {
		t.Fatal("retried uncertain run", err)
	}
	if _, err := s.Reconcile(p.ID, 1, "blue-bird", "finished"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Continue(p.ID, 1, "me", ""); err != nil {
		t.Fatal(err)
	}
	q, _ = s.Queue()
	if len(q[0].Queued) != 2 {
		t.Fatal(q)
	}
}
