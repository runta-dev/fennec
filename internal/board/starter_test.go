package board

import (
	"errors"
	"testing"
)

func TestStarterProjectOnlyOnNewBoardAndStaysDeleted(t *testing.T) {
	a, b := testStore(t), testStore(t)
	a.Role, b.Role = "local", "cloud"
	for _, s := range []Store{a, b} {
		added, err := s.AddStarterProject("My tasks", "Send requests here")
		if err != nil || !added {
			t.Fatal("starter not added to a new board", added, err)
		}
	}
	exchange(t, a, b)
	exchange(t, b, a)
	for _, s := range []Store{a, b} {
		projects, err := s.Projects()
		if err != nil || len(projects) != 1 || projects[0].ID != StarterProjectID || projects[0].Name != "My tasks" {
			t.Fatal("two devices adding the starter should share one queue", projects, err)
		}
	}
	if err := a.DeleteProject(StarterProjectID); err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	for _, s := range []Store{a, b} {
		if added, err := s.AddStarterProject("My tasks", ""); err != nil || added {
			t.Fatal("deleted starter came back", added, err)
		}
		if _, err := s.Project(StarterProjectID); !errors.Is(err, ErrNotFound) {
			t.Fatal("deleted starter returned", err)
		}
	}
	// A device joining later adds it again before syncing; the tombstone wins.
	c := testStore(t)
	c.Role = "local"
	if added, err := c.AddStarterProject("My tasks", ""); err != nil || !added {
		t.Fatal(added, err)
	}
	exchange(t, c, a)
	exchange(t, a, c)
	for _, s := range []Store{a, c} {
		if projects, err := s.Projects(); err != nil || len(projects) != 0 {
			t.Fatal("tombstone lost to a late starter", projects, err)
		}
	}
}

func TestStarterProjectSkipsBoardWithQueues(t *testing.T) {
	s := testStore(t)
	if _, err := s.CreateProject(Project{Name: "Mine"}); err != nil {
		t.Fatal(err)
	}
	if added, err := s.AddStarterProject("My tasks", ""); err != nil || added {
		t.Fatal("starter added next to an existing queue", added, err)
	}
}
