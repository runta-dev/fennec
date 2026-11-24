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

func TestWorkingOrBlockedAgentNeverStalls(t *testing.T) {
	for _, status := range []string{"working", "blocked", ""} {
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
