package queuescheduler

import (
	"testing"
	"time"

	"fennec/internal/board"
)

// runningOnCloud dispatches one request on the cloud and returns it running in
// a live session that the test then describes through ex.sessions.
func runningOnCloud(t *testing.T) (Scheduler, *executorFixture, board.Project, board.Request) {
	t.Helper()
	store := board.Store{Home: t.TempDir(), Role: "cloud"}
	p, err := store.CreateProject(board.Project{Name: "Work", Queue: 1, Workers: []board.Worker{{Agent: "claude", On: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetRouting(board.Routing{Locations: map[string]string{"claude": "cloud"}}); err != nil {
		t.Fatal(err)
	}
	r, _ := store.CreateRequest(p.ID, "me", "Tag threads", "", "")
	ex := &executorFixture{}
	s := Scheduler{Store: store, Executor: ex}
	step(t, s)
	if r, err = store.Request(p.ID, r.Number); err != nil || r.Status != board.Working || r.Runs[len(r.Runs)-1].State != "running" {
		t.Fatalf("not running: %+v %v", r, err)
	}
	return s, ex, p, r
}

func agentIs(ex *executorFixture, r board.Request, status string, at time.Time) {
	ex.sessions = []Session{{ID: r.Session, State: "live", Status: status, StatusAt: at}}
}

// later moves the scheduler's clock forward from the run's delivery.
func later(s *Scheduler, d time.Duration) time.Time {
	at := time.Now().Add(d)
	s.Now = func() time.Time { return at }
	return at
}

func TestAgentThatStoppedWithoutReplyingStalls(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	// THI-17: the agent ended its turn waiting for a build that never
	// reported back, and the request showed as running for 13 hours.
	agentIs(ex, r, "done", time.Now().Add(time.Minute))
	later(&s, StallAfter+2*time.Minute)
	step(t, s)
	got, _ := s.Store.Request(p.ID, r.Number)
	if got.Status != board.NeedsYou || got.Failure != board.StalledFailure {
		t.Fatalf("still shown as running: %+v", got)
	}
}

func TestBrieflyIdleAgentKeepsRunning(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	// Stopped 20 minutes in (e.g. to wait for a build); seen 5 minutes later.
	agentIs(ex, r, "done", time.Now().Add(20*time.Minute))
	later(&s, 25*time.Minute)
	step(t, s)
	if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.Working {
		t.Fatalf("stalled within the grace period: %+v", got)
	}
}

func TestWorkingAgentNeverStalls(t *testing.T) {
	for _, status := range []string{"working", ""} {
		s, ex, p, r := runningOnCloud(t)
		agentIs(ex, r, status, time.Now().Add(time.Minute))
		later(&s, 24*time.Hour)
		step(t, s)
		if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.Working {
			t.Fatalf("%q agent stalled: %+v", status, got)
		}
	}
}

func TestIdleSinceBeforeDeliveryCountsFromDelivery(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	// The agent's idle status predates the prompt: its age says nothing about
	// how long the run has been waiting, which is only seconds.
	agentIs(ex, r, "idle", time.Now().Add(-24*time.Hour))
	step(t, s)
	if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.Working {
		t.Fatalf("stalled a run delivered seconds ago: %+v", got)
	}
	// THI-39: a prompt that never started a turn leaves the agent idle from
	// before delivery. Once the grace period passes, that run is not running.
	later(&s, StallAfter+time.Minute)
	step(t, s)
	if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.NeedsYou {
		t.Fatalf("never-started run still shown as running: %+v", got)
	}
}

func TestStalledAgentThatResumesIsRunningAgain(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	agentIs(ex, r, "done", time.Now().Add(time.Minute))
	at := later(&s, StallAfter+2*time.Minute)
	step(t, s)
	if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.NeedsYou {
		t.Fatalf("not stalled: %+v", got)
	}
	agentIs(ex, r, "working", at)
	step(t, s)
	got, _ := s.Store.Request(p.ID, r.Number)
	if got.Status != board.Working || got.Failure != "" || got.Runs[len(got.Runs)-1].State != "running" {
		t.Fatalf("resumed agent not running: %+v", got)
	}
	if ex.starts != 1 {
		t.Fatalf("resuming relaunched the agent: %d starts", ex.starts)
	}
}

func TestStalledRunLetsTheNextRequestStart(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	next, _ := s.Store.CreateRequest(p.ID, "me", "Next", "", "")
	step(t, s)
	if got, _ := s.Store.Request(p.ID, next.Number); got.Status != board.Queued {
		t.Fatalf("queue limit ignored: %+v", got)
	}
	agentIs(ex, r, "done", time.Now().Add(time.Minute))
	later(&s, StallAfter+2*time.Minute)
	step(t, s) // stall
	step(t, s) // dispatch into the freed slot
	if got, _ := s.Store.Request(p.ID, next.Number); got.Status != board.Working {
		t.Fatalf("phantom run still holds the slot: %+v", got)
	}
}

// THI-69: the agent's terminal ended mid-run (Herdr's default listing hides
// finished terminals, so the scheduler never saw it). The request must stop
// showing as running and release its slot.
func TestRunWhoseSessionFinishedIsNotRunning(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	ex.sessions = []Session{{ID: r.Session, State: "finished", Status: "working"}}
	step(t, s)
	if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.Failed {
		t.Fatalf("finished session still shown as running: %+v", got)
	}
}

func TestEarlierFinishedTerminalDoesNotHideTheLiveOne(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	// A reused thread session keeps its earlier, finished terminal around.
	for _, order := range [][]string{{"live", "finished"}, {"finished", "live"}} {
		ex.sessions = []Session{{ID: r.Session, State: order[0], Status: "working"}, {ID: r.Session, State: order[1], Status: "working"}}
		step(t, s)
		if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.Working {
			t.Fatalf("%v: live run treated as ended: %+v", order, got)
		}
	}
}

func TestRunWhoseSessionVanishedIsNotRunning(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	ex.sessions = nil
	step(t, s)
	if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.Working {
		t.Fatalf("absent right after launch must wait for the listing: %+v", got)
	}
	later(&s, StallAfter+time.Minute)
	step(t, s)
	if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.Failed {
		t.Fatalf("vanished session still shown as running: %+v", got)
	}
}

func TestUnavailableListingNeverEndsARun(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	ex.unavailable = true
	later(&s, 24*time.Hour)
	_ = s.Step(t.Context())
	if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.Working {
		t.Fatalf("failed listing ended a run: %+v", got)
	}
}

func TestDoneClaudeCodeStallsQuickly(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	// THI-141: THI-125's agent said it would continue after an SSH check it
	// never started. Claude Code shows working while anything it started in
	// the background is open, so done means nothing will wake it.
	stopped := time.Now().Add(time.Minute)
	ex.sessions = []Session{{ID: r.Session, State: "live", Status: "done", App: "claude-code", StatusAt: stopped}}
	s.Now = func() time.Time { return stopped.Add(QuietStallAfter - 10*time.Second) }
	step(t, s)
	if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.Working {
		t.Fatalf("stalled before the quiet grace period: %+v", got)
	}
	s.Now = func() time.Time { return stopped.Add(QuietStallAfter + 10*time.Second) }
	step(t, s)
	if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.NeedsYou || got.Failure != board.StalledFailure {
		t.Fatalf("done Claude Code still shown as running: %+v", got)
	}
}

func TestClaudeCodeWithBackgroundWorkNeverStalls(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	ex.sessions = []Session{{ID: r.Session, State: "live", Status: "working", App: "claude-code", Background: true, StatusAt: time.Now()}}
	later(&s, 24*time.Hour)
	step(t, s)
	if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.Working {
		t.Fatalf("agent waiting on a background shell stalled: %+v", got)
	}
}

func TestAgentApp(t *testing.T) {
	if got := AgentApp([]byte(`{"app":"claude-code","state":"done"}`)); got != "claude-code" {
		t.Fatalf("AgentApp = %q", got)
	}
}

// THI-163: an agent that asks a question in its session (Claude Code's
// AskUserQuestion; herdr-core status "blocked") needs the person right away,
// and is running again once the answer lets it work.
func TestAskingAgentNeedsYouUntilAnswered(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	agentIs(ex, r, "blocked", time.Now())
	step(t, s)
	got, _ := s.Store.Request(p.ID, r.Number)
	if got.Status != board.NeedsYou || got.Failure != board.AskingFailure || !got.Asking() {
		t.Fatalf("asking agent still shown as running: %+v", got)
	}
	// Still asking a day later: stays a question, never becomes a stall.
	later(&s, 24*time.Hour)
	step(t, s)
	if got, _ = s.Store.Request(p.ID, r.Number); got.Failure != board.AskingFailure {
		t.Fatalf("open question changed: %+v", got)
	}
	agentIs(ex, r, "working", time.Now())
	step(t, s)
	got, _ = s.Store.Request(p.ID, r.Number)
	if got.Status != board.Working || got.Failure != "" || got.Runs[len(got.Runs)-1].State != "running" || ex.starts != 1 {
		t.Fatalf("answered agent not running: %+v (%d starts)", got, ex.starts)
	}
}

func TestDismissedQuestionBecomesAStall(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	agentIs(ex, r, "blocked", time.Now())
	step(t, s)
	// The question was dismissed (Esc): the agent's turn is over.
	agentIs(ex, r, "idle", time.Now())
	step(t, s)
	got, _ := s.Store.Request(p.ID, r.Number)
	if got.Status != board.NeedsYou || got.Failure != board.StalledFailure || got.Runs[len(got.Runs)-1].State != "stalled" {
		t.Fatalf("dismissed question still shown as asking: %+v", got)
	}
}

func TestAgentReplyAnswersAskingRun(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	agentIs(ex, r, "blocked", time.Now())
	step(t, s)
	if _, err := s.Store.Complete(p.ID, r.Number, r.Session, r.Agent, "done", board.Resolved); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Store.Request(p.ID, r.Number); got.Status != board.Review {
		t.Fatalf("reply after a question not kept: %+v", got)
	}
}
