package board

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeleteQueueReplicatesAndSurvivesOfflineEdits(t *testing.T) {
	a, b := testStore(t), testStore(t)
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
		if projects, err := s.Projects(); err != nil || len(projects) != 0 {
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
