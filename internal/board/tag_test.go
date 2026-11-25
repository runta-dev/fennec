package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAgentTagsRequestNobodyTagged(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery"})
	r, err := s.CreateRequest(p.ID, "me", "Support tags on threads", "", "")
	if err != nil || r.Tag != "bug" || !r.TagAuto {
		t.Fatal("untagged request should get the first tag, marked automatic", r, err)
	}
	if _, err = s.Claim(p.ID, r.Number, "calm-fox", "codex"); err != nil {
		t.Fatal(err)
	}
	claimed, _ := s.Request(p.ID, r.Number)
	prompt := RunPrompt(p, claimed, "fennec-terminal queue")
	if !strings.Contains(prompt, "fennec-terminal queue tag "+p.ID) || !strings.Contains(prompt, "feature (Feature)") {
		t.Fatal("prompt does not ask the agent to tag", prompt)
	}

	if _, err = s.AutoTag(p.ID, r.Number, "blue-bird", "codex", "feature"); !errors.Is(err, ErrConflict) {
		t.Fatal("another session tagged the request", err)
	}
	if _, err = s.AutoTag(p.ID, r.Number, "calm-fox", "codex", "chore"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown tag accepted", err)
	}
	before, _ := s.Request(p.ID, r.Number)
	r, err = s.AutoTag(p.ID, r.Number, "calm-fox", "codex", "Feature") // a label works too
	if err != nil || r.Tag != "feature" || !r.TagAuto || !r.Updated.Equal(before.Updated) {
		t.Fatal(r, err)
	}

	// A person's choice clears the mark and stands.
	if r, err = s.Change(p.ID, r.Number, []string{"discussion"}, ""); err != nil || r.TagAuto {
		t.Fatal(r, err)
	}
	if _, err = s.AutoTag(p.ID, r.Number, "calm-fox", "codex", "bug"); !errors.Is(err, ErrConflict) {
		t.Fatal("agent overrode a person's tag", err)
	}
	if prompt = RunPrompt(p, r, "fennec-terminal queue"); strings.Contains(prompt, " tag ") {
		t.Fatal("prompt asks to tag a request a person tagged", prompt)
	}
}

func TestChosenTagIsNotAutomatic(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery"})
	r, err := s.CreateRequest(p.ID, "me", "Crash on launch", "bug", "")
	if err != nil || r.TagAuto {
		t.Fatal(r, err)
	}
}

func TestQueueTagCommand(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery"})
	r, _ := s.CreateRequest(p.ID, "me", "Add dark mode", "", "")
	s.Claim(p.ID, r.Number, "calm-fox", "claude")
	var out bytes.Buffer
	if err := AgentCommand(s.Home, []string{"tag", p.ID, "1", "claude", "calm-fox", "feature"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	var got Request
	if json.Unmarshal(out.Bytes(), &got) != nil || got.Tag != "feature" {
		t.Fatal(out.String())
	}
	if err := AgentCommand(s.Home, []string{"tag", p.ID, "1", "claude", "calm-fox", "nope"}, nil, &out); err == nil || !strings.Contains(err.Error(), "unknown tag") {
		t.Fatal(err)
	}
}

// THI-133: a request can have several tags, kept in the project's order, with
// Tag the first so builds that know only Tag still see one.
func TestRequestHasSeveralTags(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery", Tags: []Tag{{"bug", "Bug"}, {"feature", "Feature"}, {"desktop", "Desktop"}, {"ios", "iOS"}}})
	r, err := s.CreateRequestWithAttachments(p.ID, "me", "Chip clipped", []string{"Desktop", "bug", "desktop"}, "", nil)
	if err != nil || r.TagAuto || r.Tag != "bug" || !slices.Equal(r.TagList(), []string{"bug", "desktop"}) {
		t.Fatal(r, err)
	}
	if r, err = s.Change(p.ID, r.Number, []string{"ios", "feature", "desktop"}, ""); err != nil || r.Tag != "feature" || !slices.Equal(r.Tags, []string{"feature", "desktop", "ios"}) {
		t.Fatal(r, err)
	}
	if _, err = s.Change(p.ID, r.Number, []string{}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("a request lost all its tags", err)
	}
	if _, err = s.Change(p.ID, r.Number, []string{"bug", "chore"}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown tag accepted", err)
	}
	// Back to one tag: stored as before THI-133.
	if r, err = s.Change(p.ID, r.Number, []string{"bug"}, ""); err != nil || r.Tags != nil || !slices.Equal(r.TagList(), []string{"bug"}) {
		t.Fatal(r, err)
	}
	// A build without Tags changing Tag wins over stale Tags.
	if !slices.Equal((Request{Tag: "ios", Tags: []string{"bug", "desktop"}}).TagList(), []string{"ios"}) {
		t.Fatal("stale Tags kept")
	}

	r, _ = s.Change(p.ID, r.Number, []string{"bug", "desktop"}, "")
	thread := Thread(p, r)
	if !strings.Contains(thread, "Tags: Bug, Desktop\n") {
		t.Fatal(thread)
	}
	if err = s.MirrorThreads(p.ID); err != nil {
		t.Fatal(err)
	}
	index, _ := os.ReadFile(filepath.Join(s.ThreadsPath(p.ID), "INDEX.md"))
	mirror, _ := os.ReadFile(filepath.Join(s.ThreadsPath(p.ID), p.Key+"-1.md"))
	if !strings.Contains(string(index), "[bug,desktop] Chip clipped") || !strings.Contains(string(mirror), "tags: [bug, desktop]\n") {
		t.Fatal(string(index), string(mirror))
	}
}

func TestAgentSetsSeveralTags(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery", Tags: []Tag{{"bug", "Bug"}, {"feature", "Feature"}, {"desktop", "Desktop"}}})
	r, _ := s.CreateRequest(p.ID, "me", "Tag menu", "", "")
	s.Claim(p.ID, r.Number, "calm-fox", "claude")
	claimed, _ := s.Request(p.ID, r.Number)
	if prompt := RunPrompt(p, claimed, "fennec-terminal queue"); !strings.Contains(prompt, "<tag>[,<tag>...]") {
		t.Fatal(prompt)
	}
	var out bytes.Buffer
	if err := AgentCommand(s.Home, []string{"tag", p.ID, "1", "claude", "calm-fox", "desktop,Feature"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	var got Request
	if json.Unmarshal(out.Bytes(), &got) != nil || !slices.Equal(got.TagList(), []string{"feature", "desktop"}) || !got.TagAuto {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := AgentCommand(s.Home, []string{"new", p.ID, "1", "claude", "calm-fox", "bug,desktop"}, strings.NewReader("Chip overlaps"), &out); err != nil {
		t.Fatal(err)
	}
	var filed Request
	if json.Unmarshal(out.Bytes(), &filed) != nil || !slices.Equal(filed.TagList(), []string{"bug", "desktop"}) || filed.TagAuto {
		t.Fatal(out.String())
	}
}

func TestPatchRequestTags(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery", Tags: []Tag{{"bug", "Bug"}, {"desktop", "Desktop"}}})
	s.CreateRequest(p.ID, "me", "Tag menu", "", "")
	h := s.Handler()
	for _, c := range []struct {
		body string
		want []string
	}{{`{"tags":["desktop","bug"]}`, []string{"bug", "desktop"}}, {`{"tag":"desktop"}`, []string{"desktop"}}, {`{"status":""}`, []string{"desktop"}}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "http://board.fennec/v1/projects/"+p.ID+"/requests/1", strings.NewReader(c.body)))
		var got Request
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || !slices.Equal(got.TagList(), c.want) || got.TagAuto {
			t.Fatal(c.body, w.Code, w.Body.String())
		}
	}
}
