package board

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueueNewFilesARequestForThePerson(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery"})
	r, _ := s.CreateRequest(p.ID, "me", "Fix the wait card", "", "")
	s.Claim(p.ID, r.Number, "calm-fox", "claude")
	var out bytes.Buffer
	in := strings.NewReader("Add the waiting card on iOS\nSame card as the desktop thread.\n")
	if err := AgentCommand(s.Home, []string{"new", p.ID, "1", "claude", "calm-fox", "Feature"}, in, &out); err != nil {
		t.Fatal(err)
	}
	var got Request
	if json.Unmarshal(out.Bytes(), &got) != nil || got.Number == r.Number {
		t.Fatal(out.String())
	}
	got, _ = s.Request(p.ID, got.Number)
	if got.Title != "Add the waiting card on iOS" || got.Tag != "feature" || got.TagAuto || got.Author != "me" || got.Status != Queued ||
		len(got.Messages) != 1 || got.Messages[0].Body != "Same card as the desktop thread." || got.Messages[0].Agent != "" {
		t.Fatalf("%+v", got)
	}
	origin, _ := s.Request(p.ID, r.Number)
	if len(origin.Links) != 1 || origin.Links[0].Number != got.Number || origin.Links[0].Kind != LinkRelated {
		t.Fatalf("not linked: %+v", origin.Links)
	}

	out.Reset()
	if err := AgentCommand(s.Home, []string{"new", p.ID, "1", "claude", "calm-fox", "auto"}, strings.NewReader("Untagged follow-up"), &out); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(out.Bytes(), &got)
	if !got.TagAuto || got.Messages[0].Body != "Untagged follow-up" {
		t.Fatalf("%+v", got)
	}
	if err := AgentCommand(s.Home, []string{"new", p.ID, "1", "claude", "other-run", "auto"}, strings.NewReader("x"), &out); err == nil || !strings.Contains(err.Error(), "no longer owns") {
		t.Fatal("a run that doesn't own the request filed one", err)
	}
	if err := AgentCommand(s.Home, []string{"new", p.ID, "1", "claude", "calm-fox", "nope"}, strings.NewReader("x"), &out); err == nil || !strings.Contains(err.Error(), "unknown tag") {
		t.Fatal(err)
	}
	if err := AgentCommand(s.Home, []string{"new", p.ID, "1", "claude", "calm-fox", "auto"}, strings.NewReader("\n\n"), &out); err == nil {
		t.Fatal("filed a request without a title")
	}
}

// Agents that run on the Mac reach the board through its HTTP API.
func TestFileRequestOverHTTP(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery"})
	r, _ := s.CreateRequest(p.ID, "me", "Fix the wait card", "", "")
	s.Claim(p.ID, r.Number, "calm-fox", "claude")
	body := `{"agent":"claude","session":"calm-fox","tag":"bug","text":"Card flickers\nOn iOS."}`
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "http://board.fennec/v1/projects/"+p.ID+"/requests/1/file", strings.NewReader(body)))
	var got Request
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Title != "Card flickers" || got.Tag != "bug" || got.Author != "me" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "http://board.fennec/v1/projects/"+p.ID+"/requests/1/file", strings.NewReader(strings.Replace(body, "calm-fox", "old-run", 1))))
	if w.Code == http.StatusOK {
		t.Fatal("a stale run filed a request")
	}
}
