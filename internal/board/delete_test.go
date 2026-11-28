package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeleteQueueReplicatesAndSurvivesOfflineEdits(t *testing.T) {
	a, b := testStore(t), testStore(t)
	// The board keeps its last project (FEN-231), so it needs a second one.
	if _, err := a.CreateProject(Project{Name: "Keep"}); err != nil {
		t.Fatal(err)
	}
	p, err := a.CreateProject(Project{Name: "Delete me"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateRequest(p.ID, "me", "Pending", "", ""); err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	response := httptest.NewRecorder()
	a.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "http://board.fennec/v1/projects/"+p.ID, nil))
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if _, err := b.UpdateProject(p.ID, json.RawMessage(`{"name":"Offline rename"}`)); err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	exchange(t, b, a)
	for _, s := range []Store{a, b} {
		if _, err := s.Project(p.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("deleted queue returned", err)
		}
		if projects, err := s.Projects(); err != nil || len(projects) != 1 || projects[0].Name != "Keep" {
			t.Fatal(projects, err)
		}
		if _, err := s.Requests(p.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("deleted requests returned", err)
		}
		if _, err := s.UpdateProject(p.ID, json.RawMessage(`{"deleted":false}`)); !errors.Is(err, ErrNotFound) {
			t.Fatal("resurrected queue", err)
		}
		next, err := s.CreateProject(Project{Name: "Delete me"})
		if err != nil || next.ID == p.ID {
			t.Fatal("reused deleted identity", next, err)
		}
	}
}

// FEN-5: a request deleted from its queue's context menu stays deleted on
// every replica, frees its place, and keeps its number.
func TestDeleteRequestReplicatesAndSurvivesOfflineEdits(t *testing.T) {
	a, b := testStore(t), testStore(t)
	p, err := a.CreateProject(Project{Name: "Requests"})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := a.CreateRequest(p.ID, "me", "Delete me", "", "")
	if err != nil {
		t.Fatal(err)
	}
	kept, err := a.CreateRequest(p.ID, "me", "Keep me", "", "")
	if err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	response := httptest.NewRecorder()
	a.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodDelete, fmt.Sprintf("http://board.fennec/v1/projects/%s/requests/%d", p.ID, gone.Number), nil))
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if _, err := b.Reply(p.ID, gone.Number, "me", "", "Offline reply"); err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	exchange(t, b, a)
	for _, s := range []Store{a, b} {
		if _, err := s.Request(p.ID, gone.Number); !errors.Is(err, ErrNotFound) {
			t.Fatal("deleted request returned", err)
		}
		list, err := s.Requests(p.ID)
		if err != nil || len(list) == 0 || list[0].Number != kept.Number {
			t.Fatal(list, err)
		}
		for _, item := range list {
			if item.Number == gone.Number {
				t.Fatal("deleted request listed", list)
			}
		}
		if _, err := s.Reply(p.ID, gone.Number, "me", "", "Again"); !errors.Is(err, ErrNotFound) {
			t.Fatal("replied to deleted request", err)
		}
		if err := s.DeleteRequest(p.ID, gone.Number); !errors.Is(err, ErrNotFound) {
			t.Fatal("deleted twice", err)
		}
		next, err := s.CreateRequest(p.ID, "me", "Next", "", "")
		if err != nil || next.Number == gone.Number {
			t.Fatal("reused deleted number", next, err)
		}
		exchange(t, a, b)
		exchange(t, b, a)
	}
}
