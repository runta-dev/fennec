// Package queuescheduler is shared by the cloud-primary service and the Mac
// backup. It chooses work; the executor always uses herdr-core sessions.
package queuescheduler

import (
	"context"
	"crypto/rand"
	"errors"
	"fennec/internal/board"
	r "fennec/internal/runtime"
	"fmt"
	"sort"
	"time"
)

// Session is a herdr-core terminal: its fennec.session label, state and OSC title.
type Session struct{ ID, State, Title string }
type Executor interface {
	List(context.Context) ([]Session, error)
	Start(context.Context, board.Project, board.Request, string) error
}
type Scheduler struct {
	Store    board.Store
	Executor Executor
	Command  string
	OnError  func(error)
}

func (s Scheduler) report(err error) {
	if err != nil && !errors.Is(err, board.ErrConflict) && s.OnError != nil {
		s.OnError(err)
	}
}

func (s Scheduler) startFailed(project string, number int, session string, err error) error {
	if errors.Is(err, ErrInputUnconfirmed) {
		_, err = s.Store.FailInput(project, number, session)
		return err
	}
	if errors.Is(err, r.ErrAgentExecutableMissing) {
		_, err = s.Store.Reconcile(project, number, session, "not_started")
		return err
	}
	_, err = s.Store.Fail(project, number, session)
	return err
}
func (s Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s.report(s.Step(ctx))
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s Scheduler) Step(ctx context.Context) error {
	if err := s.Store.Recover(); err != nil {
		return err
	}
	if err := s.Store.QueuePendingFollowUps(); err != nil {
		return err
	}
	queues, err := s.Store.Queue()
	if err != nil {
		return err
	}
	hasFollowUp := func(q board.QueueProject) bool {
		for _, r := range q.Queued {
			if len(r.Runs) > 0 {
				return true
			}
		}
		return false
	}
	sort.SliceStable(queues, func(i, j int) bool { return hasFollowUp(queues[i]) && !hasFollowUp(queues[j]) })
	var states map[string]string
	// A failed listing is never treated as an empty authoritative snapshot.
	listed, err := s.Executor.List(ctx)
	if err != nil {
		return err
	}
	states, titles := map[string]string{}, map[string]string{}
	for _, v := range listed {
		states[v.ID] = v.State
		titles[v.ID] = v.Title
	}
	routing := s.Store.Routing()
	control := s.Store.LocalControl()
	for _, q := range queues {
		for _, r := range q.Active {
			if r.RunLocation != s.Store.Role {
				continue
			}
			state := states[r.ExecutionSession()]
			if state == "live" {
				// The thread takes the name the agent gives its session.
				if title := board.SessionTitle(titles[r.ExecutionSession()], r.ExecutionSession(), fmt.Sprintf("%s-%d", q.Key, r.Number)); title != "" && title != r.Title {
					s.report(s.Store.Retitle(q.Project, r.Number, r.ExecutionSession(), title))
				}
			}
			if r.Status == board.Working && len(r.Runs) > 0 && r.Runs[len(r.Runs)-1].State == "starting" {
				p, e := s.Store.Project(q.Project)
				if e != nil {
					return e
				}
				attempt, cancel := context.WithTimeout(ctx, 2*time.Minute)
				e = s.Executor.Start(attempt, p, r, board.RunPrompt(p, r, s.Command))
				cancel()
				if e != nil {
					s.report(e)
					e = s.startFailed(p.ID, r.Number, r.Session, e)
				} else {
					_, e = s.Store.Reconcile(p.ID, r.Number, r.Session, "live")
				}
				s.report(e)
				continue
			}
			if state == "finished" || (state == "live" && (r.Status == board.Unknown || len(r.Runs) > 0 && r.Runs[len(r.Runs)-1].State == "starting")) {
				_, err := s.Store.Reconcile(q.Project, r.Number, r.Session, state)
				s.report(err)
			}
		}
		if q.Paused {
			continue
		}
		p, err := s.Store.Project(q.Project)
		if err != nil {
			return err
		}
		var worker board.Worker
		for _, w := range q.Workers {
			if w.On {
				worker = w
				break
			}
		}
		if worker.Agent == "" {
			continue
		}
		target := routing.Locations[worker.Agent]
		if target == "" {
			target = "local"
		}
		// Only a connected Mac returns idle local slots to the cloud; offline backup
		// keeps its allocation. The cloud can give idle slots to the Mac at any time.
		if target != s.Store.Role && (s.Store.Role == "cloud" || control.Primary) {
			s.report(s.Store.TransferIdle(p.ID, target))
		}
		for _, request := range q.Queued {
			worker := worker
			for _, w := range q.Workers {
				if w.On && w.Agent == request.Agent {
					worker = w
					break
				}
			}
			target := routing.Locations[worker.Agent]
			if target == "" {
				target = "local"
			}
			if target != s.Store.Role && (s.Store.Role == "cloud" || control.Primary) {
				s.report(s.Store.TransferIdle(p.ID, target))
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if target != s.Store.Role {
				if s.Store.Role == "cloud" && target == "local" && control.Primary {
					s.report(s.Store.Propose(p.ID, request.Number, worker.Agent, control.Epoch))
				}
				continue
			}
			if !s.Store.AttachmentsReady(request) {
				continue
			}
			id := newRunID()
			if s.Store.Role == "local" && control.Primary {
				if request.Offer == nil || request.Offer.Epoch != control.Epoch || request.Offer.Agent != worker.Agent {
					continue
				}
				id = request.Offer.ID
			}
			claimed, err := s.Store.ClaimAt(p.ID, request.Number, id, worker.Agent, target)
			if errors.Is(err, board.ErrConflict) {
				continue
			}
			if err != nil {
				return err
			}
			prompt := board.RunPrompt(p, claimed, s.Command)
			attempt, cancel := context.WithTimeout(ctx, 2*time.Minute)
			err = s.Executor.Start(attempt, p, claimed, prompt)
			cancel()
			if err != nil {
				s.report(err)
				err = s.startFailed(p.ID, claimed.Number, id, err)
				s.report(err)
				continue
			}
			_, err = s.Store.Reconcile(p.ID, claimed.Number, id, "live")
			s.report(err)
		}
	}
	return nil
}
func newRunID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", id[:])
}
