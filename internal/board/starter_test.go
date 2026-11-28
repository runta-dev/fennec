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
	mine, err := a.CreateProject(Project{Name: "Mine"})
	if err != nil {
		t.Fatal(err)
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
		if projects, err := s.Projects(); err != nil || len(projects) != 1 || projects[0].ID != mine.ID {
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

// FEN-231: Fennec always keeps at least one project, so there is no empty
// "create a project" page.
func TestLastProjectCannotBeDeleted(t *testing.T) {
	s := testStore(t)
	if _, err := s.AddStarterProject("My tasks", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(StarterProjectID); !errors.Is(err, ErrLastProject) || !errors.Is(err, ErrConflict) {
		t.Fatal("deleted the last project", err)
	}
	other, err := s.CreateProject(Project{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(StarterProjectID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(other.ID); !errors.Is(err, ErrLastProject) {
		t.Fatal("deleted the last project", err)
	}
	if projects, err := s.Projects(); err != nil || len(projects) != 1 || projects[0].ID != other.ID {
		t.Fatal(projects, err)
	}
}

// Two replicas each delete one of the last two projects while apart; the
// board ends up empty, and the starter comes back under a fresh id, which
// both replicas agree on.
func TestStarterReturnsWhenReplicasDeleteTheLastProjects(t *testing.T) {
	a, b := testStore(t), testStore(t)
	a.Role, b.Role = "local", "cloud"
	if _, err := a.AddStarterProject("My tasks", ""); err != nil {
		t.Fatal(err)
	}
	other, err := a.CreateProject(Project{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	exchange(t, b, a)
	if err := a.DeleteProject(StarterProjectID); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteProject(other.ID); err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	exchange(t, b, a)
	if a.HasProjects() || b.HasProjects() {
		t.Fatal("expected an empty board after both deletes")
	}
	for _, s := range []Store{a, b} {
		if added, err := s.AddStarterProject("My tasks", ""); err != nil || !added {
			t.Fatal("starter not put back", added, err)
		}
	}
	exchange(t, a, b)
	exchange(t, b, a)
	for _, s := range []Store{a, b} {
		projects, err := s.Projects()
		if err != nil || len(projects) != 1 || projects[0].ID != StarterProjectID+"-2" {
			t.Fatal("replicas should share one replacement starter", projects, err)
		}
	}
}
