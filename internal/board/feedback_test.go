package board

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestFeedbackOpensATaggedRequestAndJoinsThreads(t *testing.T) {
	s := testStore(t)
	if _, err := s.CreateProject(Project{Name: "Nebula"}); err != nil {
		t.Fatal(err)
	}
	edward := External{Name: "Edward", Channel: "discord", ReplyRef: "dc:123/456",
		Context: &FeedbackContext{Version: "1.4.1", Device: "iPhone 11", OS: "iOS 18.2"}}
	r, err := s.PostFeedback("nebula", edward, "Boss phase two crashes", "Crashed halfway through the boss fight, third time!")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != Queued || r.TagAuto || !slices.Equal(r.TagList(), []string{"feedback", "discord"}) || r.Author != "Edward" {
		t.Fatalf("%+v", r)
	}
	m := r.Messages[0]
	if m.External == nil || m.External.ReplyRef != "dc:123/456" || m.Author != "Edward" || m.Agent != "" {
		t.Fatalf("%+v", m)
	}
	p, _ := s.Project("nebula")
	if _, ok := TagFor(p, "Feedback"); !ok {
		t.Fatal(p.Tags)
	}

	// A person tags it a bug; a widget user's Feedback joins the thread.
	if _, err = s.Change("nebula", r.Number, []string{"bug", "feedback", "discord"}, ""); err != nil {
		t.Fatal(err)
	}
	anon := External{Name: "Anonymous #a91f", Channel: "widget", Context: &FeedbackContext{Version: "1.4.1", Device: "iPhone SE 2"}}
	r, err = s.AddFeedback("nebula", r.Number, anon, "Game crashes at boss phase 2")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.TagList(), []string{"bug", "feedback", "discord", "widget"}) || r.Status != Queued || len(r.Messages) != 2 {
		t.Fatalf("%+v", r)
	}
	if p, _ = s.Project("nebula"); len(p.Tags) != 6 {
		t.Fatal(p.Tags)
	}

	// Agents see who is external, never the reply handle.
	text := Thread(p, r)
	if !strings.Contains(text, "--- Edward (external user via Discord; iPhone 11 · iOS 18.2 · 1.4.1), ") ||
		!strings.Contains(text, "--- Anonymous #a91f (external user via Widget; iPhone SE 2 · 1.4.1), ") ||
		strings.Contains(text, "dc:123") {
		t.Fatal(text)
	}
	if mirror := threadMarkdown(p, r, nil); strings.Contains(mirror, "dc:123") || !strings.Contains(mirror, "## Edward (external user via Discord") {
		t.Fatal(mirror)
	}

	// Clients get the author as JSON.
	data, _ := json.Marshal(r.Messages[1])
	if !strings.Contains(string(data), `"external":{"name":"Anonymous #a91f","channel":"widget","context":{"version":"1.4.1","device":"iPhone SE 2"}}`) {
		t.Fatal(string(data))
	}
}

func TestFeedbackRejectsUnknownChannelsAndBadNames(t *testing.T) {
	s := testStore(t)
	if _, err := s.CreateProject(Project{Name: "Nebula"}); err != nil {
		t.Fatal(err)
	}
	for _, from := range []External{
		{Name: "Edward", Channel: "email"},
		{Name: "", Channel: "discord"},
		{Name: "Ed\nward", Channel: "discord"},
		{Name: "Edward", Channel: "x", Context: &FeedbackContext{Device: "a\nb"}},
		{Name: "Edward", Channel: "x", ReplyRef: strings.Repeat("r", maxReplyRef+1)},
	} {
		if _, err := s.PostFeedback("nebula", from, "Title", "body"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", from, err)
		}
	}
	if p, _ := s.Project("nebula"); len(p.Tags) != len(DefaultTags) {
		t.Fatal("rejected Feedback added tags", p.Tags)
	}
}

func TestPeopleAndAgentsKeepTheirNames(t *testing.T) {
	if who := (Message{Author: "shiqi"}).Who(); who != "shiqi" {
		t.Fatal(who)
	}
	if who := (Message{Author: "claude", Agent: "claude"}).Who(); who != "claude (agent)" {
		t.Fatal(who)
	}
}
