package queuescheduler

import (
	"context"
	"errors"
	"fennec/internal/board"
	"fennec/internal/runtime"
	"testing"
)

type executorFixture struct {
	sessions    []Session
	starts      int
	lost        bool
	unavailable bool
}

func (e *executorFixture) List(context.Context) ([]Session, error) {
	if e.unavailable {
		return nil, errors.New("offline")
	}
	return e.sessions, nil
}
func (e *executorFixture) Start(_ context.Context, _ board.Project, r board.Request, _ string) error {
	e.starts++
	e.sessions = append(e.sessions, Session{r.Session, "live"})
	if e.lost {
		return errors.New("receipt lost")
	}
	return nil
}
func syncStores(t *testing.T, a, b board.Store) {
	t.Helper()
	left, err := a.Sync(board.Exchange{})
	if err != nil {
		t.Fatal(err)
	}
	right, err := b.Sync(left)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Sync(right); err != nil {
		t.Fatal(err)
	}
}
func step(t *testing.T, s Scheduler) {
	t.Helper()
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestCloudPrimaryUsesHerdrExecutorAndTransfersCapacity(t *testing.T) {
	local := board.Store{Home: t.TempDir(), Role: "local"}
	cloud := board.Store{Home: t.TempDir(), Role: "cloud"}
	p, _ := local.CreateProject(board.Project{Name: "Work", Queue: 1})
	request, _ := local.CreateRequest(p.ID, "me", "Build", "", "")
	local.SetRouting(board.Routing{Locations: map[string]string{"claude": "cloud"}})
	local.SetPrimary(true)
	le, ce := &executorFixture{}, &executorFixture{}
	ls := Scheduler{Store: local, Executor: le}
	cs := Scheduler{Store: cloud, Executor: ce}
	syncStores(t, local, cloud)
	step(t, ls)
	syncStores(t, local, cloud)
	step(t, cs)
	syncStores(t, cloud, local)
	if ce.starts != 1 || le.starts != 0 {
		t.Fatal(ce.starts, le.starts)
	}
	r, _ := cloud.Request(p.ID, request.Number)
	if len(r.Runs) != 1 || r.Runs[0].ID != r.Session || r.RunLocation != "cloud" {
		t.Fatal(r)
	}
	if _, err := cloud.Complete(p.ID, r.Number, r.Session, r.Agent, "Ready for review", board.NeedsYou); err != nil {
		t.Fatal(err)
	}
	syncStores(t, cloud, local)
	local.SetRouting(board.Routing{Locations: map[string]string{"claude": "local"}})
	if _, err := local.Continue(p.ID, r.Number, "me", "Continue here"); err != nil {
		t.Fatal(err)
	}
	syncStores(t, local, cloud)
	step(t, cs)
	syncStores(t, cloud, local)
	step(t, ls)
	result, _ := local.Request(p.ID, r.Number)
	if le.starts != 1 || ce.starts != 1 || len(result.Runs) != 2 || result.Runs[0].ID == result.Runs[1].ID {
		t.Fatal(result, le.starts, ce.starts)
	}
}
func TestBackupFencesLateCloudOfferAndDoesNotDuplicateUnknownRun(t *testing.T) {
	local := board.Store{Home: t.TempDir(), Role: "local"}
	cloud := board.Store{Home: t.TempDir(), Role: "cloud"}
	p, _ := local.CreateProject(board.Project{Name: "Backup", Queue: 1})
	r, _ := local.CreateRequest(p.ID, "me", "Work", "", "")
	local.SetPrimary(true)
	local.SetRouting(board.Routing{Locations: map[string]string{"claude": "local"}})
	syncStores(t, local, cloud)
	ce, le := &executorFixture{}, &executorFixture{lost: true}
	cs := Scheduler{Store: cloud, Executor: ce}
	ls := Scheduler{Store: local, Executor: le}
	step(t, cs) // Offer is in flight when the connection is lost.
	local.SetPrimary(false)
	step(t, ls)
	r, _ = local.Request(p.ID, r.Number)
	if r.Status != board.Unknown || le.starts != 1 {
		t.Fatal(r, le.starts)
	}
	syncStores(t, cloud, local)
	step(t, ls)
	step(t, cs)
	r, _ = local.Request(p.ID, r.Number)
	if r.Status != board.Working || le.starts != 1 || ce.starts != 0 {
		t.Fatal(r, le.starts, ce.starts)
	}
	// A restart with no authoritative observation must not retry the launch.
	le.unavailable = true
	if err := ls.Step(context.Background()); err == nil {
		t.Fatal("offline listing accepted")
	}
	if le.starts != 1 {
		t.Fatal(le.starts)
	}
}
func TestIndependentOfflineSlotsRespectGlobalLimit(t *testing.T) {
	local := board.Store{Home: t.TempDir(), Role: "local"}
	cloud := board.Store{Home: t.TempDir(), Role: "cloud"}
	p, _ := local.CreateProject(board.Project{Name: "Capacity", Queue: 2})
	local.SetPrimary(false)
	local.SetRouting(board.Routing{Locations: map[string]string{"claude": "local"}})
	if err := local.TransferIdle(p.ID, "cloud"); err != nil {
		t.Fatal(err)
	}
	syncStores(t, local, cloud)
	// Preferences can differ while disconnected; each endpoint is still bounded
	// by its persisted slot ownership, regardless of its stale peer observations.
	cloud.SetRouting(board.Routing{Locations: map[string]string{"claude": "cloud"}})
	for i := 0; i < 3; i++ {
		local.CreateRequest(p.ID, "me", "Local work", "", "")
		cloud.CreateRequest(p.ID, "me", "Cloud work", "", "")
	}
	le, ce := &executorFixture{}, &executorFixture{}
	step(t, Scheduler{Store: local, Executor: le})
	step(t, Scheduler{Store: cloud, Executor: ce})
	if le.starts != 1 || ce.starts != 1 {
		t.Fatal("capacity not conserved", le.starts, ce.starts)
	}
	syncStores(t, local, cloud)
	q, _ := local.Queue()
	if q[0].Working != 2 {
		t.Fatal(q)
	}
}

func TestRestartFinishesClaimedButNeverSentRun(t *testing.T) {
	store := board.Store{Home: t.TempDir(), Role: "local"}
	p, _ := store.CreateProject(board.Project{Name: "Restart", Queue: 1})
	r, _ := store.CreateRequest(p.ID, "me", "Work", "", "")
	store.SetPrimary(false)
	store.SetRouting(board.Routing{Locations: map[string]string{"claude": "local"}})
	run := "1234567890abcdef1234567890abcdef"
	if _, err := store.ClaimAt(p.ID, r.Number, run, "claude", "local"); err != nil {
		t.Fatal(err)
	}
	executor := &executorFixture{}
	step(t, Scheduler{Store: store, Executor: executor})
	got, _ := store.Request(p.ID, r.Number)
	if executor.starts != 1 || got.Session != run || got.Status != board.Working {
		t.Fatal(got, executor.starts)
	}
	step(t, Scheduler{Store: store, Executor: executor})
	if executor.starts != 1 {
		t.Fatal("duplicate recovery", executor.starts)
	}
}

func TestRejectedStartIsRetryableButLostReceiptStaysUnknown(t *testing.T) {
	for _, missing := range []bool{true, false} {
		store := board.Store{Home: t.TempDir(), Role: "cloud"}
		p, err := store.CreateProject(board.Project{Name: "Work", Queue: 1})
		if err != nil {
			t.Fatal(err)
		}
		request, err := store.CreateRequest(p.ID, "me", "Build", "", "")
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := store.ClaimAt(p.ID, request.Number, "test-run", "claude", "cloud")
		if err != nil {
			t.Fatal(err)
		}
		failure := errors.New("lost spawn receipt")
		if missing {
			failure = runtime.ErrAgentExecutableMissing
		}
		s := Scheduler{Store: store}
		if err := s.startFailed(p.ID, request.Number, claimed.Session, failure); err != nil {
			t.Fatal(err)
		}
		got, _ := store.Request(p.ID, request.Number)
		want := board.Unknown
		if missing {
			want = board.Failed
		}
		if got.Status != want {
			t.Fatal(got.Status, want)
		}
		_, err = store.Continue(p.ID, request.Number, "me", "")
		if missing && err != nil || !missing && !errors.Is(err, board.ErrConflict) {
			t.Fatal(err)
		}
	}
}
