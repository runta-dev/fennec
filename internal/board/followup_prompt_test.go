package board

import (
	"strings"
	"testing"
)

func TestFollowUpPromptIsOnlyTheNewMessages(t *testing.T) {
	store := Store{Home: t.TempDir()}
	p, _ := store.CreateProject(Project{Name: "Thread", Queue: 1})
	r, _ := store.CreateRequest(p.ID, "me", "Question", "", "First ask, long")
	if FollowUpPrompt(p, r, "fen") != "" {
		t.Fatal("a request with no runs has no follow-up form")
	}
	r, _ = store.ClaimAt(p.ID, r.Number, "calm-fox", "claude", "cloud")
	if FollowUpPrompt(p, r, "fen") != "" {
		t.Fatal("the first run must get the full prompt")
	}
	store.Complete(p.ID, r.Number, "calm-fox", "claude", "A very long agent reply", NeedsYou)
	store.FollowUp(p.ID, r.Number, "me", "\u53d1\u65b0\u7684 testflight", nil)
	r, err := store.ClaimAt(p.ID, r.Number, "green-leaf", "claude", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	got := FollowUpPrompt(p, r, "fen")
	if !strings.HasPrefix(got, "\u53d1\u65b0\u7684 testflight\n\n") || !strings.Contains(got, "fen reply "+p.ID+" 1 claude green-leaf resolved|you|failed") {
		t.Fatalf("%q", got)
	}
	for _, old := range []string{"First ask", "agent reply", "Queue knowledge"} {
		if strings.Contains(got, old) {
			t.Fatalf("resent %q: %q", old, got)
		}
	}
	prompts := RunPrompts(p, r, "fen")
	if prompts.For(true) != got || prompts.For(false) != RunPrompt(p, r, "fen") {
		t.Fatal("For must pick the follow-up only for a continued conversation")
	}
}

func TestFollowUpPromptRepeatsInputTheAgentNeverAnswered(t *testing.T) {
	store := Store{Home: t.TempDir()}
	p, _ := store.CreateProject(Project{Name: "Thread", Queue: 1})
	r, _ := store.CreateRequest(p.ID, "me", "Question", "", "Ask")
	store.ClaimAt(p.ID, r.Number, "calm-fox", "claude", "cloud")
	store.Complete(p.ID, r.Number, "calm-fox", "claude", "Reply", NeedsYou)
	store.FollowUp(p.ID, r.Number, "me", "one", nil)
	store.ClaimAt(p.ID, r.Number, "green-leaf", "claude", "cloud")
	store.Reconcile(p.ID, r.Number, "green-leaf", "live")
	if f, err := store.Reconcile(p.ID, r.Number, "green-leaf", "finished"); err != nil || f.Status != Failed {
		t.Fatal(f.Status, err)
	}
	store.FollowUp(p.ID, r.Number, "me", "two", nil)
	r, err := store.ClaimAt(p.ID, r.Number, "blue-sky", "claude", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if got := FollowUpPrompt(p, r, "fen"); !strings.HasPrefix(got, "one\n\ntwo\n\n") || strings.Contains(got, "Ask") {
		t.Fatalf("%q", got)
	}
}

func TestFollowUpPromptNeedsAReplyFromThisAgentAndLocation(t *testing.T) {
	store := Store{Home: t.TempDir()}
	p, _ := store.CreateProject(Project{Name: "Thread", Queue: 1})
	r, _ := store.CreateRequest(p.ID, "me", "Question", "", "Ask")
	store.ClaimAt(p.ID, r.Number, "calm-fox", "claude", "cloud")
	store.Complete(p.ID, r.Number, "calm-fox", "claude", "Reply", NeedsYou)
	store.FollowUp(p.ID, r.Number, "me", "more", nil)
	r, err := store.ClaimAt(p.ID, r.Number, "green-leaf", "codex", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if got := FollowUpPrompt(p, r, "fen"); got != "" {
		t.Fatalf("another agent has none of the thread: %q", got)
	}
}
