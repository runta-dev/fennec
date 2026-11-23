package board

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func exchange(t *testing.T, a, b Store) {
	t.Helper()
	left, err := a.Sync(Exchange{})
	if err != nil {
		t.Fatal(err)
	}
	right, err := b.Sync(left)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Sync(right); err != nil {
		t.Fatal(err)
	}
}
func TestReplicaOfflineCommentsSettingsAndReplay(t *testing.T) {
	a := testStore(t)
	b := testStore(t)
	a.Role = "local"
	b.Role = "cloud"
	p, _ := a.CreateProject(Project{Name: "Work"})
	r, _ := a.CreateRequest(p.ID, "me", "Original", "", "")
	exchange(t, a, b)
	a.Reply(p.ID, r.Number, "me", "", "Mac comment")
	b.Reply(p.ID, r.Number, "me", "", "Cloud comment")
	a.UpdateProject(p.ID, json.RawMessage(`{"paused":true}`))
	b.UpdateProject(p.ID, json.RawMessage(`{"description":"Keep this instruction"}`))
	ar, _ := a.CreateRequest(p.ID, "me", "Mac offline", "", "")
	br, _ := b.CreateRequest(p.ID, "me", "Cloud offline", "", "")
	if ar.Number == br.Number {
		t.Fatal("offline number collision")
	}
	exchange(t, a, b)
	exchange(t, b, a)
	exchange(t, a, b)
	for _, store := range []Store{a, b} {
		got, err := store.Request(p.ID, r.Number)
		if err != nil || len(got.Messages) != 3 {
			t.Fatal(got, err)
		}
		project, _ := store.Project(p.ID)
		if !project.Paused || project.Description != "Keep this instruction" {
			t.Fatal(project)
		}
		list, _ := store.Requests(p.ID)
		if len(list) != 3 {
			t.Fatal(list)
		}
	}
	// A crash after the journal fsync but before publishing a projection recovers.
	os.Remove(filepath.Join(a.root(), "replica-applied.json"))
	os.Remove(a.requestPath(p.ID, r.Number))
	if _, err := a.Sync(Exchange{}); err != nil {
		t.Fatal(err)
	}
	got, _ := a.Request(p.ID, r.Number)
	if len(got.Messages) != 3 {
		t.Fatal(got)
	}
}
func TestReplicaRejectsTraversal(t *testing.T) {
	s := testStore(t)
	_, err := s.Sync(Exchange{Events: []Mutation{{Version: Version{1, "01234567890123456789012345678901"}, Object: "../private.json", Fields: map[string]json.RawMessage{"x": json.RawMessage(`true`)}}}})
	if err == nil {
		t.Fatal("accepted traversal")
	}
}
