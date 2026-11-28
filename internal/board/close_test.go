package board

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func workingRun(t *testing.T) (Store, Project, Request) {
	t.Helper()
	store := Store{Home: t.TempDir()}
	p, err := store.CreateProject(Project{Name: "Close", Queue: 1})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := store.CreateRequest(p.ID, "me", "Spins after login", "", "Stuck on the loading screen")
	if r, err = store.Claim(p.ID, r.Number, "calm-fox", "claude"); err != nil {
		t.Fatal(err)
	}
	return store, p, r
}

// FEN-220: an agent closes a request that needed no code change itself,
// with a reason the thread shows; it does not wait for review.
func TestAgentClosesWithReason(t *testing.T) {
	store, p, r := workingRun(t)
	if _, err := store.Close(p.ID, r.Number, "calm-fox", "claude", "", "  "); !errors.Is(err, ErrInvalid) {
		t.Fatal("closed without a reason", err)
	}
	if _, err := store.Complete(p.ID, r.Number, "calm-fox", "claude", "Closing", Done); !errors.Is(err, ErrInvalid) {
		t.Fatal("Complete closed without a reason", err)
	}
	if _, err := store.Close(p.ID, r.Number, "other-run", "claude", "", "Duplicate"); !errors.Is(err, ErrConflict) {
		t.Fatal("a stale run closed the request", err)
	}
	got, err := store.Close(p.ID, r.Number, "calm-fox", "claude", "Same as FEN-1; fixed in 1.4.2.", " Already fixed in 1.4.2. ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Resolved || !got.Reclaim || got.Closed == nil {
		t.Fatalf("not closed: %+v", got)
	}
	if c := got.Closed; c.By != "claude" || c.Run != "calm-fox" || c.Reason != "Already fixed in 1.4.2." || c.At.IsZero() {
		t.Fatalf("closure: %+v", c)
	}
	if last := got.Messages[len(got.Messages)-1]; last.Agent != "claude" || last.Body != "Same as FEN-1; fixed in 1.4.2." || got.Delivered != last.ID {
		t.Fatalf("last message: %+v (delivered %d)", last, got.Delivered)
	}
	if run := got.Runs[len(got.Runs)-1]; run.State != Resolved || run.Finished == nil {
		t.Fatalf("run: %+v", run)
	}
	// A retried command changes nothing.
	again, err := store.Close(p.ID, r.Number, "calm-fox", "claude", "Same as FEN-1; fixed in 1.4.2.", "Already fixed in 1.4.2.")
	if err != nil || len(again.Messages) != len(got.Messages) {
		t.Fatal("repeat close", err, len(again.Messages))
	}
	// Reopening clears it.
	reopened, err := store.Change(p.ID, r.Number, nil, NeedsYou)
	if err != nil || reopened.Closed != nil || reopened.Status != NeedsYou {
		t.Fatalf("reopen: %+v %v", reopened.Closed, err)
	}
}

func TestAgentCloseWithoutMessage(t *testing.T) {
	store, p, r := workingRun(t)
	before := len(r.Messages)
	got, err := store.Close(p.ID, r.Number, "calm-fox", "claude", "", "Spam")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != before || got.Closed == nil || got.Closed.Reason != "Spam" {
		t.Fatalf("messages %d, closure %+v", len(got.Messages), got.Closed)
	}
	// The person writing again puts it back in the queue, unclosed.
	got, err = store.FollowUp(p.ID, r.Number, "me", "It still happens on 1.4.2", nil)
	if err != nil || got.Status != Queued || got.Closed != nil {
		t.Fatalf("follow-up: %s %+v %v", got.Status, got.Closed, err)
	}
}

// A message that arrived while the agent was closing wins: the request goes
// back to the agent, and the reason stays in the thread.
func TestAgentCloseWithPendingFollowUp(t *testing.T) {
	store, p, r := workingRun(t)
	if _, err := store.FollowUp(p.ID, r.Number, "me", "Wait, it's on 1.4.2", nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.Close(p.ID, r.Number, "calm-fox", "claude", "", "Already fixed")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Queued || got.Closed != nil || got.Messages[len(got.Messages)-1].Body != "Already fixed" {
		t.Fatalf("closed over a pending message: %s %+v", got.Status, got.Closed)
	}
}

func TestQueueReplyDoneCommand(t *testing.T) {
	store, p, r := workingRun(t)
	n := strconv.Itoa(r.Number)
	var out bytes.Buffer
	err := AgentCommand(store.Home, []string{"reply", p.ID, n, "claude", "calm-fox", "done"}, strings.NewReader(""), &out)
	if err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatal("done without a reason", err)
	}
	if err = AgentCommand(store.Home, []string{"reply", p.ID, n, "claude", "calm-fox", "resolved", "extra"}, strings.NewReader("x"), &out); err == nil {
		t.Fatal("resolved took a reason")
	}
	if err = AgentCommand(store.Home, []string{"reply", p.ID, n, "claude", "calm-fox", "done", "Not a bug"}, strings.NewReader("Works as designed.\n"), &out); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Request(p.ID, r.Number)
	if got.Status != Resolved || got.Closed == nil || got.Closed.Reason != "Not a bug" || got.Messages[len(got.Messages)-1].Body != "Works as designed." {
		t.Fatalf("%s %+v", got.Status, got.Closed)
	}
}

func TestRunPromptTellsWhenToClose(t *testing.T) {
	_, p, r := workingRun(t)
	got := RunPrompt(p, r, "fennec-terminal queue")
	if !strings.Contains(got, "fennec-terminal queue reply "+p.ID+" "+strconv.Itoa(r.Number)+" claude calm-fox done \"<reason>\"") || !strings.Contains(got, "If you changed code, never close it") {
		t.Fatal(got)
	}
}

// A Feedback request that needed no code change is closed with a reason
// without a release (FEN-230's gate is only for resolved), and the
// Feedback prompt names the command.
func TestFeedbackRequestClosesWithoutRelease(t *testing.T) {
	s, p := feedbackStore(t)
	r, _ := s.Request(p.ID, 1)
	if prompt := RunPrompt(p, r, "fen queue"); !strings.Contains(prompt, "fen queue reply "+p.ID+" 1 claude calm-fox done \"<reason>\"") {
		t.Fatal(prompt)
	}
	got, err := s.Close(p.ID, 1, "calm-fox", "claude", "", "Not a bug: works as designed")
	if err != nil || got.Status != Resolved || got.Closed == nil {
		t.Fatalf("%s %+v %v", got.Status, got.Closed, err)
	}
}

func TestQueueReplyTooShortIsUsage(t *testing.T) {
	store, p, r := workingRun(t)
	err := AgentCommand(store.Home, []string{"reply", p.ID, strconv.Itoa(r.Number)}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatal(err)
	}
}
