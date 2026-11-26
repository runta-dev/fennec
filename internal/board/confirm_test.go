package board

import (
	"errors"
	"testing"
)

func unconfirmedRun(t *testing.T) (Store, Project, Request) {
	t.Helper()
	store := Store{Home: t.TempDir()}
	p, err := store.CreateProject(Project{Name: "Confirm", Queue: 1})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := store.CreateRequest(p.ID, "me", "Question", "", "")
	if r, err = store.Claim(p.ID, r.Number, "calm-fox", "claude"); err != nil {
		t.Fatal(err)
	}
	if r, err = store.FailInput(p.ID, r.Number, "calm-fox"); err != nil || r.Status != Unknown {
		t.Fatal(r.Status, err)
	}
	return store, p, r
}

func TestWorkingAgentConfirmsUnconfirmedInput(t *testing.T) {
	store, p, r := unconfirmedRun(t)
	if _, err := store.ConfirmStarted(p.ID, r.Number, "other-run"); !errors.Is(err, ErrConflict) {
		t.Fatal("confirmed a different run", err)
	}
	got, err := store.ConfirmStarted(p.ID, r.Number, "calm-fox")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Working || got.Failure != "" || got.Runs[len(got.Runs)-1].State != "running" || got.Runs[len(got.Runs)-1].Finished != nil {
		t.Fatalf("not running: %+v", got)
	}
	if _, err = store.ConfirmStarted(p.ID, r.Number, "calm-fox"); !errors.Is(err, ErrConflict) {
		t.Fatal("confirmed twice", err)
	}
}

func TestAgentReplyClearsUnconfirmedInputNote(t *testing.T) {
	store, p, r := unconfirmedRun(t)
	got, err := store.Complete(p.ID, r.Number, "calm-fox", "claude", "Done", Resolved)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Review || got.Failure != "" {
		t.Fatalf("stale failure kept: %q %q", got.Status, got.Failure)
	}
}

// FEN-11: a launch that failed before any input was sent is not an
// unconfirmed input delivery, even when the thread has images.
func TestLaunchFailureOnImageThreadIsNotUnconfirmedInput(t *testing.T) {
	s := testStore(t)
	a, err := s.SaveAttachment(attachmentFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.CreateProject(Project{Name: "Input"})
	r, err := s.CreateRequestWithAttachments(p.ID, "me", "Check image", nil, "", []Attachment{a})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(p.ID, r.Number, "calm-fox", "claude"); err != nil {
		t.Fatal(err)
	}
	if r, err = s.Fail(p.ID, r.Number, "calm-fox"); err != nil || r.Status != Unknown {
		t.Fatal(r.Status, err)
	}
	if state := r.Runs[len(r.Runs)-1].State; state == "input_unknown" {
		t.Fatalf("launch failure awaits input confirmation: %q", state)
	}
}
