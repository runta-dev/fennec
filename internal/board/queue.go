package board

import (
	"path/filepath"
	"sort"
	"time"
)

type Run struct {
	ID       string     `json:"id"`
	Session  string     `json:"session"`
	Location string     `json:"location"`
	State    string     `json:"state"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
}

func recordRun(r *Request, state string, now time.Time) {
	if len(r.Runs) == 0 {
		return
	}
	run := &r.Runs[len(r.Runs)-1]
	run.State = state
	if state == Resolved || state == Failed || state == NeedsYou {
		run.Finished = &now
	}
}

func sortRequests(list []Summary, order []int) {
	rank := map[int]int{}
	for i, n := range order {
		rank[n] = i + 1
	}
	group := func(status string) int {
		switch status {
		case NeedsYou:
			return 0
		case Failed:
			return 1
		case Working:
			return 2
		case Queued:
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if group(a.Status) != group(b.Status) {
			return group(a.Status) < group(b.Status)
		}
		if a.Status == Queued {
			ar, br := rank[a.Number], rank[b.Number]
			if ar != br {
				if ar == 0 {
					return false
				}
				if br == 0 {
					return true
				}
				return ar < br
			}
			at, bt := a.QueuedAt, b.QueuedAt
			if at.IsZero() {
				at = a.Created
			}
			if bt.IsZero() {
				bt = b.Created
			}
			if !at.Equal(bt) {
				return at.Before(bt)
			}
			return a.Number < b.Number
		}
		if !a.Updated.Equal(b.Updated) {
			return a.Updated.After(b.Updated)
		}
		return a.Number < b.Number
	})
}

// Move inserts one waiting request before another (zero means the front).
// A single project-file write commits the order, including concurrent arrivals.
func (s Store) Move(project string, n, before int) (Project, error) {
	unlock, err := s.lock()
	if err != nil {
		return Project{}, err
	}
	defer unlock()
	p, err := s.Project(project)
	if err != nil {
		return p, err
	}
	list, err := s.Requests(project)
	if err != nil {
		return p, err
	}
	found, target := false, before == 0
	order := []int{}
	for _, r := range list {
		if r.Status != Queued {
			continue
		}
		if r.Number == n {
			found = true
			continue
		}
		if r.Number == before {
			target = true
		}
		order = append(order, r.Number)
	}
	if !found || !target || n == before {
		return p, ErrConflict
	}
	result := []int{}
	if before == 0 {
		result = append(result, n)
	}
	for _, id := range order {
		if id == before {
			result = append(result, n)
		}
		result = append(result, id)
	}
	p.Order = result
	return p, s.commit(filepath.Join(s.projectDir(project), "project.json"), p)
}

// Continue enqueues an explicit user instruction, including a retry with no
// new text. Running and completed requests must not be scheduled twice.
func (s Store) Continue(project string, n int, author, body string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if r.Status != NeedsYou && r.Status != Failed {
		return r, ErrConflict
	}
	if !validText(author, 80) || (body != "" && !validText(body, maxBody)) || len(r.Messages) >= 2000 {
		return r, ErrInvalid
	}
	now := s.now()
	if body != "" {
		id := len(r.Messages) + 1
		r.Messages = append(r.Messages, Message{UID: randomID(), ID: id, Author: author, Body: body, At: now})
	}
	r.Status, r.Failure, r.Updated, r.QueuedAt = Queued, "", now, now
	r.Session, r.RunLocation, r.Agent, r.Offer = "", "", "", nil
	return r, s.commit(s.requestPath(project, n), r)
}

// Complete fences late replies against the currently assigned session.
func (s Store) Complete(project string, n int, session, agent, body, outcome string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if r.Session != session || r.Agent != agent {
		return r, ErrConflict
	}
	if r.Status == outcome {
		for _, m := range r.Messages {
			if m.RunID == session && m.Agent == agent && m.Body == body {
				return r, nil
			}
		}
	}
	if r.Status != Working && r.Status != Unknown {
		return r, ErrConflict
	}
	if outcome != NeedsYou && outcome != Resolved && outcome != Failed {
		return r, ErrInvalid
	}
	if !validText(body, maxBody) || len(r.Messages) >= 2000 {
		return r, ErrInvalid
	}
	now := s.now()
	id := len(r.Messages) + 1
	r.Messages = append(r.Messages, Message{UID: randomID(), RunID: session, ID: id, Author: agent, Agent: agent, Body: body, At: now})
	r.Status, r.Updated, r.Delivered = outcome, now, id
	recordRun(&r, outcome, now)
	return r, s.commit(s.requestPath(project, n), r)
}

// Fail retains the session identity after an uncertain mutation. Only an
// explicit retry may inspect and reuse it; a lost receipt never auto-launches.
func (s Store) Fail(project string, n int, session string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if r.Session != session || r.Status != Working {
		return r, ErrConflict
	}
	r.Status, r.Failure, r.Updated = Unknown, "Execution could not be confirmed", s.now()
	recordRun(&r, Unknown, r.Updated)
	if len(RequestAttachments(r)) > 0 {
		recordRun(&r, "input_unknown", r.Updated)
	}
	return r, s.commit(s.requestPath(project, n), r)
}

// Reconcile accepts only a positive observation of the same session. Missing
// sessions and network failures remain unknown, never automatic retries.
func (s Store) Reconcile(project string, n int, session, state string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if r.Session != session || (r.Status != Working && r.Status != Unknown) {
		return r, ErrConflict
	}
	switch state {
	case "live":
		if len(r.Runs) > 0 && r.Runs[len(r.Runs)-1].State == "input_unknown" {
			return r, ErrConflict
		}
		r.Status = Working
		r.Failure = ""
		recordRun(&r, "running", s.now())
	case "finished":
		r.Status = Failed
		r.Failure = "The session ended without a result"
		recordRun(&r, Failed, s.now())
	default:
		return r, ErrInvalid
	}
	r.Updated = s.now()
	return r, s.commit(s.requestPath(project, n), r)
}
