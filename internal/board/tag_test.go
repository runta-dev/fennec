package board

import (
	"bytes"
	"encoding/json"
	"errors"
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
	if r, err = s.Change(p.ID, r.Number, "discussion", ""); err != nil || r.TagAuto {
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
