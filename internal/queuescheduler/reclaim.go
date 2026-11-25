package queuescheduler

import (
	"context"
	"time"

	"fennec/internal/board"
)

// Ender ends a terminal. Executors that can end their sessions reclaim the
// sessions of requests a person marked done.
type Ender interface {
	End(ctx context.Context, address string) error
}

// reclaim ends the agent sessions of requests marked done (board.Request.Reclaim).
// It only touches sessions this scheduler's location runs, never one an
// unfinished request still uses, and never an agent that is working: that
// session is ended after its turn. Once every session is gone the flag clears,
// but only on the side that ran the request last (Request.RunLocation): both
// schedulers see the flag, and the other side has nothing of its own to end.
//
// It also ends superseded sessions: a request that moved to a new session (a
// local run retried in the cloud, or the other way round) keeps the earlier
// terminal open, so the sidebar shows more sessions than the queue has threads.
// Only the session of a request's latest run is kept.
func (s Scheduler) reclaim(ctx context.Context, listed []Session) {
	ender, ok := s.Executor.(Ender)
	if !ok {
		return
	}
	projects, err := s.Store.Projects()
	if err != nil {
		s.report(err)
		return
	}
	type item struct {
		project string
		request board.Summary
	}
	inUse := map[string]bool{}
	current := map[string]bool{}
	pending := []item{}
	all := []board.Summary{}
	for _, p := range projects {
		list, err := s.Store.Requests(p.ID)
		if err != nil {
			s.report(err)
			return
		}
		for _, r := range list {
			all = append(all, r)
			current[r.ExecutionSession()] = true
			if r.Status == board.Resolved {
				if r.Reclaim {
					pending = append(pending, item{p.ID, r})
				}
				continue
			}
			inUse[r.Session] = true
			for _, run := range r.Runs {
				inUse[run.Session] = true
			}
		}
	}
	live := map[string][]Session{}
	for _, v := range listed {
		if v.State == "live" && v.ID != "" {
			live[v.ID] = append(live[v.ID], v)
		}
	}
	end := func(t Session) bool {
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := ender.End(attempt, t.Address)
		cancel()
		if err != nil {
			s.report(err)
			return false
		}
		return true
	}
	ended := map[string]bool{}
	for _, r := range all {
		for _, run := range r.Runs {
			if run.Location != s.Store.Role || run.Session == "" || current[run.Session] || ended[run.Session] {
				continue
			}
			ended[run.Session] = true
			for _, t := range live[run.Session] {
				if t.Status != "working" && t.Address != "" {
					end(t)
				}
			}
		}
	}
	if len(pending) == 0 {
		return
	}
	for _, it := range pending {
		done := true
		seen := map[string]bool{}
		for _, run := range it.request.Runs {
			if run.Location != s.Store.Role || run.Session == "" || seen[run.Session] || inUse[run.Session] {
				continue
			}
			seen[run.Session] = true
			for _, t := range live[run.Session] {
				if t.Status == "working" || t.Address == "" {
					done = false
					continue
				}
				if !end(t) {
					done = false
				}
			}
		}
		owner := it.request.RunLocation
		if done && (owner == "" || owner == s.Store.Role) {
			s.report(s.Store.Reclaimed(it.project, it.request.Number))
		}
	}
}
