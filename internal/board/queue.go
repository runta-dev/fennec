package board

import (
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Run struct {
	ID         string     `json:"id"`
	Agent      string     `json:"agent,omitempty"`
	InputAfter int        `json:"input_after,omitempty"`
	InputUIDs  []string   `json:"input_uids,omitempty"`
	Session    string     `json:"session"`
	Location   string     `json:"location"`
	State      string     `json:"state"`
	Started    time.Time  `json:"started"`
	Finished   *time.Time `json:"finished,omitempty"`
}

// ExecutionSession is independent of the run token used to fence late replies.
func (r Request) ExecutionSession() string {
	if len(r.Runs) > 0 {
		return r.Runs[len(r.Runs)-1].Session
	}
	return r.Session
}

func (s Store) BindSession(project string, n int, run, session string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return err
	}
	if !sessionRef.MatchString(session) {
		return ErrInvalid
	}
	if r.Session != run || len(r.Runs) == 0 {
		return ErrConflict
	}
	if r.Runs[len(r.Runs)-1].Session == session {
		return nil
	}
	r.Runs[len(r.Runs)-1].Session = session
	r.Updated = s.now()
	return s.commit(s.requestPath(project, n), r)
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
		case Review:
			return 2
		case Working:
			return 3
		case Queued:
			return 4
		default:
			return 5
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if group(a.Status) != group(b.Status) {
			return group(a.Status) < group(b.Status)
		}
		if a.Status == Queued {
			if (len(a.Runs) > 0) != (len(b.Runs) > 0) {
				return len(a.Runs) > 0
			}
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

// Continue retains the CLI's explicit empty-retry operation. The forum uses
// FollowUp with text and/or images.
func (s Store) Continue(project string, n int, author, body string) (Request, error) {
	return s.FollowUp(project, n, author, body, nil)
}

// Retry is the explicit retry an unconfirmed, failed or needs-you request
// waits for: Fennec never relaunches those by itself (see Fail and
// Reconcile). The request goes back to the queue as it is, without new
// input, and the scheduler starts it in a new session; a late reply from the
// old session no longer matches the request and is refused (see Complete).
// Requests that are queued, running, awaiting review or done are left alone.
func (s Store) Retry(project string, n int) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if r.Status != Unknown && r.Status != Failed && r.Status != NeedsYou {
		return r, ErrConflict
	}
	now := s.now()
	r.Status, r.Failure, r.Updated, r.QueuedAt = Queued, "", now, now
	r.Reclaim = false
	r.Closed = nil
	clearQuestion(&r)
	r.Session, r.RunLocation, r.Offer = "", "", nil
	return r, s.commit(s.requestPath(project, n), r)
}

// FollowUp persists input before scheduling it. Input arriving during a turn
// stays pending until that turn releases its slot.
func (s Store) FollowUp(project string, n int, author, body string, attachments []Attachment) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if r.Status == Unknown && body == "" && len(attachments) == 0 {
		return r, ErrConflict
	}
	if !validText(author, 80) || (body != "" && !validText(body, maxBody)) || len(r.Messages) >= 2000 {
		return r, ErrInvalid
	}
	images, err := s.validateAttachments(attachments)
	if err != nil {
		return r, err
	}
	now := s.now()
	if body != "" || len(images) > 0 {
		id := len(r.Messages) + 1
		r.Messages = append(r.Messages, Message{UID: randomID(), FollowUp: true, ID: id, Author: author, Body: body, Attachments: images, At: now})
	}
	if r.Status == Working || r.Status == Unknown {
		r.Updated = now
		return r, s.commit(s.requestPath(project, n), r)
	}
	r.Status, r.Failure, r.Updated, r.QueuedAt = Queued, "", now, now
	r.Reclaim = false
	r.Closed = nil
	clearQuestion(&r)
	r.Session, r.RunLocation, r.Offer = "", "", nil
	return r, s.commit(s.requestPath(project, n), r)
}

// Complete fences late replies against the currently assigned session.
func (s Store) Complete(project string, n int, session, agent, body, outcome string) (Request, error) {
	if outcome == Done {
		// Closing needs a reason; that goes through Close.
		return Request{}, ErrInvalid
	}
	return s.complete(project, n, session, agent, body, outcome, "")
}

// Close is an agent marking its request done itself, which it may do only
// when the request needed no code change: already fixed, a duplicate, spam,
// not a bug (FEN-220). Work that changed code goes to Review through
// Complete, for the owner to check. The reason is required; the thread ends
// with who closed the request, when and why. Body, if any, is the agent's
// last message before that.
func (s Store) Close(project string, n int, session, agent, body, reason string) (Request, error) {
	reason = strings.TrimSpace(reason)
	if !validText(reason, maxReason) {
		return Request{}, ErrInvalid
	}
	return s.complete(project, n, session, agent, strings.TrimSpace(body), Done, reason)
}

func (s Store) complete(project string, n int, session, agent, body, outcome, reason string) (Request, error) {
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
	// An agent cannot mark a request done; its finished work awaits review.
	status := outcome
	switch outcome {
	case Resolved:
		status = Review
	case Done:
		status = Resolved
		// A repeated close (a retried command) changes nothing.
		if c := r.Closed; r.Status == Resolved && c != nil && c.By == agent && c.Run == session && c.Reason == reason {
			return r, nil
		}
	}
	if r.Status == status && outcome != Done {
		for _, m := range r.Messages {
			if m.RunID == session && m.Agent == agent && m.Body == body {
				return r, nil
			}
		}
	}
	// A stalled run's agent may still answer late (e.g. after a background
	// build it was waiting on); its reply supersedes the stall.
	if r.Status != Working && r.Status != Unknown && !r.Stalled() {
		return r, ErrConflict
	}
	if outcome != NeedsYou && outcome != Resolved && outcome != Failed && outcome != Done {
		return r, ErrInvalid
	}
	// Only handing over for review waits on the release and replies; a
	// close with a reason (Done) does not (FEN-230).
	if outcome == Resolved && FeedbackMissing(r) != "" {
		return r, ErrFeedbackPending
	}
	if (outcome != Done || body != "") && !validText(body, maxBody) || len(r.Messages) >= 2000 {
		return r, ErrInvalid
	}
	now := s.now()
	pending := r.HasPendingFollowUp()
	if outcome == Done && pending {
		// The person wrote while the agent was closing: the request goes
		// back to the agent instead, so the reason stays in the thread as
		// part of its message.
		body = strings.TrimSpace(body + "\n\n" + reason)
	}
	id := r.Delivered
	if body != "" {
		id = len(r.Messages) + 1
		r.Messages = append(r.Messages, Message{UID: randomID(), RunID: session, ID: id, Author: agent, Agent: agent, Body: body, At: now})
	}
	// The agent's reply proves the run started; an unconfirmed-input note is stale.
	r.Status, r.Updated, r.Failure = status, now, ""
	clearQuestion(&r)
	if !pending {
		r.Delivered = id
	}
	if outcome == Done {
		recordRun(&r, Resolved, now)
	} else {
		recordRun(&r, outcome, now)
	}
	switch {
	case pending:
		r.Status, r.QueuedAt, r.Offer = Queued, now, nil
	case outcome == Done:
		// Like a person's Mark done: the run's session ends once its agent stops.
		r.Closed = &Closure{By: agent, Run: session, Reason: reason, At: now}
		r.Reclaim = true
	}
	return r, s.commit(s.requestPath(project, n), r)
}

// Fail retains the session identity after an uncertain mutation. Only an
// explicit retry may inspect and reuse it; a lost receipt never auto-launches.
func (s Store) Fail(project string, n int, session string) (Request, error) {
	return s.fail(project, n, session, false)
}

func (s Store) FailInput(project string, n int, session string) (Request, error) {
	return s.fail(project, n, session, true)
}

func (s Store) fail(project string, n int, session string, input bool) (Request, error) {
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
	// Only an attempted input delivery is uncertain. A launch that failed
	// before any input was sent is not, even when the thread has images
	// (FEN-11: such a run waited for a confirmation that could never come).
	if input {
		recordRun(&r, "input_unknown", r.Updated)
	}
	return r, s.commit(s.requestPath(project, n), r)
}

// StalledFailure is shown on a request whose agent stopped without replying.
const StalledFailure = "The agent stopped without reporting back"

// AskingFailure is shown on a request whose agent is waiting for an answer in
// its own session (Claude Code's AskUserQuestion, a permission prompt).
const AskingFailure = "The agent is asking a question in its session"

// Stalled reports a run the scheduler stopped counting as running: its agent
// went idle without calling `queue reply` ("stalled"), or it is waiting for an
// answer in its session ("asking"). Either way a resumed agent runs again.
func (r Request) Stalled() bool {
	if r.Status != NeedsYou || len(r.Runs) == 0 {
		return false
	}
	state := r.Runs[len(r.Runs)-1].State
	return state == "stalled" || state == "asking"
}

// Asking reports a run whose agent is waiting for an answer in its session.
func (r Request) Asking() bool {
	return r.Stalled() && r.Runs[len(r.Runs)-1].State == "asking"
}

// Stall moves a running request whose agent has stopped without replying to
// needs-you, so it stops showing as running. The session is kept: a late reply
// still lands (see Complete) and a resumed agent puts it back to running (see
// ConfirmStarted). An asking run whose agent then stops (the question was
// dismissed) is stalled too.
func (s Store) Stall(project string, n int, session string) (Request, error) {
	return s.pause(project, n, session, "stalled", StalledFailure)
}

// Ask moves a running request whose agent is waiting for an answer in its
// session to needs-you (THI-163). It stays there until the agent works again
// (see ConfirmStarted) or replies.
func (s Store) Ask(project string, n int, session string) (Request, error) {
	return s.pause(project, n, session, "asking", AskingFailure)
}

func (s Store) pause(project string, n int, session, state, failure string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	running := r.Status == Working && len(r.Runs) > 0 && r.Runs[len(r.Runs)-1].State == "running"
	if r.Session != session || !running && !(state == "stalled" && r.Asking()) {
		return r, ErrConflict
	}
	now := s.now()
	r.Status, r.Failure, r.Updated = NeedsYou, failure, now
	run := &r.Runs[len(r.Runs)-1]
	run.State, run.Finished = state, &now
	if state != "asking" {
		clearQuestion(&r)
	}
	return r, s.commit(s.requestPath(project, n), r)
}

// ConfirmStarted resolves an unconfirmed input delivery once the agent is seen
// working in the run's session. A queue session starts a turn only from input,
// so working after the delivery attempt proves the input arrived; a merely
// live session proves nothing and stays unknown (see Reconcile). It also puts
// a stalled run back to running when its agent resumes on its own.
func (s Store) ConfirmStarted(project string, n int, session string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	unconfirmed := r.Status == Unknown && len(r.Runs) > 0 && r.Runs[len(r.Runs)-1].State == "input_unknown"
	if r.Session != session || !unconfirmed && !r.Stalled() {
		return r, ErrConflict
	}
	now := s.now()
	r.Status, r.Failure, r.Updated = Working, "", now
	clearQuestion(&r)
	run := &r.Runs[len(r.Runs)-1]
	run.State, run.Finished = "running", nil
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
	// An asking run's terminal can end too (THI-176): its question goes with it.
	if r.Session != session || (r.Status != Working && r.Status != Unknown && !(state == "finished" && r.Asking())) {
		return r, ErrConflict
	}
	clearQuestion(&r)
	switch state {
	case "not_started":
		// Only a pre-launch rejection proves this. An absent session in a
		// listing or a lost spawn receipt must never use this transition.
		r.Status = Failed
		r.Failure = "The agent could not start"
		recordRun(&r, Failed, s.now())
		if len(r.Runs) > 0 {
			r.Runs[len(r.Runs)-1].State = "not_started"
		}
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

// Reclaimed records that a done request's sessions have been ended.
func (s Store) Reclaimed(project string, n int) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return err
	}
	if !r.Reclaim {
		return nil
	}
	r.Reclaim = false
	return s.commit(s.requestPath(project, n), r)
}
