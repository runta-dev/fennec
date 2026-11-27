// Package queuescheduler is shared by the cloud-primary service and the Mac
// backup. It chooses work; the executor always uses herdr-core sessions.
package queuescheduler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fennec/internal/board"
	r "fennec/internal/runtime"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Session is a herdr-core terminal: its fennec.session label, state, OSC
// title, and the agent's own status word with when that word last changed.
type Session struct {
	ID, State, Title, Status string
	App                      string // herdr-core's status app, e.g. "claude-code"
	Address                  string // herdr-core terminal address, for ending it
	// Kind and Msg detail a blocked agent's status: "question" or
	// "permission", and herdr-core's one-line summary of it (THI-176).
	Kind, Msg string
	// Cwd is the terminal's working directory (finds Claude Code's transcript).
	Cwd      string
	StatusAt time.Time
	// Background: the agent finished its turn and only reports working because
	// background shells it started are still open (see BackgroundOnly).
	Background bool
}

// Busy reports whether the agent is in the middle of a turn. An agent whose
// turn ended but whose background shells are still open is not busy: those
// shells can outlive the turn forever (a wait loop whose pgrep matches its own
// command line never exits), and treating that as work kept done requests'
// sessions alive (THI-107).
func (s Session) Busy() bool {
	return s.Status == "working" && !s.Background
}

// backgroundOnly is Claude Code's status line once its turn is over while
// background shells are still open: "1 shell still running".
var backgroundOnly = regexp.MustCompile(`^\d+ shells? still running$`)

// BackgroundOnly reports whether a herdr-core status record says the agent is
// idle apart from background shells.
func BackgroundOnly(raw json.RawMessage) bool {
	var status struct {
		State string `json:"state"`
		Msg   string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &status)
	return status.State == "working" && backgroundOnly.MatchString(strings.TrimSpace(status.Msg))
}

// BackgroundRecords reports whether any of a terminal's other OSC 7501 records
// (herdr-core's "records", one per thing the program reports on) is working.
// Claude Code reports each background shell or monitor as its own record, and
// it can leave the root status at done while one is still running (THI-173:
// THI-145 and THI-160 were stalled two minutes into a real background wait).
func BackgroundRecords(raw json.RawMessage) bool {
	var records map[string]struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(raw, &records)
	for _, r := range records {
		if r.State == "working" {
			return true
		}
	}
	return false
}

// NewSession builds a Session from one herdr-core terminal listing entry. An
// agent whose turn ended but whose background record is still working counts
// as working in the background, exactly like Claude Code's "1 shell still
// running" root status: it does not stall, and a stalled run goes back to
// running, but reclaim does not wait for it (Busy is false).
func NewSession(id, address, state, title string, status, records json.RawMessage, statusMS int64) Session {
	s := Session{ID: id, Address: address, State: state, Title: title, Status: AgentStatus(status), App: AgentApp(status), StatusAt: StatusTime(statusMS), Background: BackgroundOnly(status)}
	s.Kind, s.Msg = statusDetail(status)
	if stopped(s.Status) && BackgroundRecords(records) {
		s.Status, s.Background = "working", true
	}
	return s
}

// AgentApp extracts which agent reported a herdr-core status record.
func AgentApp(raw json.RawMessage) string {
	var status struct {
		App string `json:"app"`
	}
	_ = json.Unmarshal(raw, &status)
	return status.App
}

// AgentStatus extracts the agent's status word from a herdr-core status record.
func AgentStatus(raw json.RawMessage) string {
	var status struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(raw, &status)
	return status.State
}

// StatusTime converts herdr-core's status_ms; zero when the agent never said.
func StatusTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// StallAfter is how long a running request's agent may sit idle without
// replying before the request stops showing as running. Agents end a turn
// while waiting on a background job and resume when it reports back, so this
// leaves room for that; a resumed agent puts the request back to running.
var StallAfter = 15 * time.Minute

// QuietStallAfter replaces StallAfter for agents whose "done" means nothing
// (a done root with a working background record is not done: see NewSession)
// can wake them. Claude Code reports working ("1 shell still running", "1
// monitor still running") while anything it started in the background is
// open, so a Claude Code agent that is done for this long ended its turn
// without `queue reply` and is not coming back on its own (THI-141: THI-125's
// agent said it was waiting on an SSH check it never started).
var QuietStallAfter = 2 * time.Minute

// quietWhenDone lists agents whose done status rules out background work.
var quietWhenDone = map[string]bool{"claude-code": true}

func stallAfter(app string) time.Duration {
	if quietWhenDone[app] {
		return QuietStallAfter
	}
	return StallAfter
}

// stopped reports an agent that is not working and not asking for anything:
// its turn ended.
func stopped(status string) bool { return status == "done" || status == "idle" }

// asking reports an agent waiting for an answer in its session: Claude Code's
// AskUserQuestion or a permission prompt (herdr-core status "blocked").
func asking(status string) bool { return status == "blocked" }

type Executor interface {
	List(context.Context) ([]Session, error)
	Start(context.Context, board.Project, board.Request, board.Prompt) error
}
type Scheduler struct {
	Store    board.Store
	Executor Executor
	Command  string
	OnError  func(error)
	Now      func() time.Time // nil means time.Now; tests move the clock
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
	states, titles, agents := map[string]string{}, map[string]string{}, map[string]string{}
	since := map[string]time.Time{}
	apps := map[string]string{}
	sessions := map[string]Session{}
	for _, v := range listed {
		// The listing includes finished terminals. A reused thread session can
		// have an earlier finished terminal beside its live one; live wins.
		if states[v.ID] == "live" && v.State != "live" {
			continue
		}
		states[v.ID] = v.State
		titles[v.ID] = v.Title
		agents[v.ID] = v.Status
		since[v.ID] = v.StatusAt
		apps[v.ID] = v.App
		sessions[v.ID] = v
	}
	s.reclaim(ctx, listed)
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	routing := s.Store.Routing()
	control := s.Store.LocalControl()
	for _, q := range queues {
		// A stalled run's agent that starts working again (its background job
		// reported back) is running again.
		// An asking run whose agent is no longer asking either got its answer
		// (working again) or the question was dismissed (stopped).
		for _, r := range q.Stalled {
			if r.RunLocation != s.Store.Role {
				continue
			}
			// An asking run whose terminal ended (or is gone from a listing
			// that includes finished terminals) has no question left (THI-176).
			if state := states[r.ExecutionSession()]; state != "live" {
				if r.Asking() {
					_, err := s.Store.Reconcile(q.Project, r.Number, r.Session, "finished")
					s.report(err)
				}
				continue
			}
			switch agent := agents[r.ExecutionSession()]; {
			case agent == "working":
				_, err := s.Store.ConfirmStarted(q.Project, r.Number, r.Session)
				s.report(err)
			case r.Asking() && stopped(agent):
				_, err := s.Store.Stall(q.Project, r.Number, r.Session)
				s.report(err)
			case r.Asking() && asking(agent):
				s.askingStep(ctx, q.Project, r, sessions[r.ExecutionSession()])
			}
		}
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
			// An unconfirmed delivery that the agent is now working on (its
			// Enter went through late, or someone submitted it by hand) is running.
			if r.Status == board.Unknown && state == "live" && agents[r.ExecutionSession()] == "working" && len(r.Runs) > 0 && r.Runs[len(r.Runs)-1].State == "input_unknown" {
				_, err := s.Store.ConfirmStarted(q.Project, r.Number, r.Session)
				s.report(err)
				continue
			}
			// A running request whose agent is asking a question in its
			// session needs the person now, not when the run ends (THI-163).
			if r.Status == board.Working && state == "live" && len(r.Runs) > 0 && r.Runs[len(r.Runs)-1].State == "running" && asking(agents[r.ExecutionSession()]) {
				asked, err := s.Store.Ask(q.Project, r.Number, r.Session)
				s.report(err)
				if err == nil {
					s.askingStep(ctx, q.Project, asked, sessions[r.ExecutionSession()])
				}
				continue
			}
			// A running request whose agent ended its turn without `queue
			// reply` is not running: nothing will move it on by itself. The
			// idle time counts from the later of delivery and the agent's last
			// status change, so a prompt that never started a turn stalls too.
			if r.Status == board.Working && state == "live" && len(r.Runs) > 0 && r.Runs[len(r.Runs)-1].State == "running" && stopped(agents[r.ExecutionSession()]) {
				idle := r.Runs[len(r.Runs)-1].Started
				if at := since[r.ExecutionSession()]; at.After(idle) {
					idle = at
				}
				if now.Sub(idle) >= stallAfter(apps[r.ExecutionSession()]) {
					_, err := s.Store.Stall(q.Project, r.Number, r.Session)
					s.report(err)
				}
				continue
			}
			if r.Status == board.Working && len(r.Runs) > 0 && r.Runs[len(r.Runs)-1].State == "starting" {
				p, e := s.Store.Project(q.Project)
				if e != nil {
					return e
				}
				attempt, cancel := context.WithTimeout(ctx, 2*time.Minute)
				e = s.Executor.Start(attempt, p, r, board.RunPrompts(p, r, s.Command))
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
			// A running request whose session ended, or that no longer exists at
			// all, is not running: its agent can never reply. An absent session
			// only counts once the run is old enough that a launch cannot still
			// be on its way into the listing.
			if state == "" && r.Status == board.Working && len(r.Runs) > 0 && r.Runs[len(r.Runs)-1].State == "running" && now.Sub(r.Runs[len(r.Runs)-1].Started) >= StallAfter {
				state = "finished"
			}
			// An unconfirmed run whose session never appeared (its launch
			// failed) can never be confirmed: nobody can be working in it.
			// Show it as failed so it can be retried, instead of awaiting
			// confirmation forever (FEN-11). The listing includes finished
			// terminals, so absent means it never existed or was removed.
			if state == "" && r.Status == board.Unknown && len(r.Runs) > 0 && now.Sub(r.Runs[len(r.Runs)-1].Started) >= StallAfter {
				state = "finished"
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
			chosen := false
			for _, w := range q.Workers {
				if w.On && w.Agent == request.Agent {
					worker, chosen = w, true
					break
				}
			}
			// A request posted for an agent that isn't one of the queue's
			// turned-on workers (FEN-110: the iPhone's + menu picks any agent
			// ready on the cloud computer) still goes to that agent. Only
			// before its first run: a request that ran keeps the queue's workers.
			if !chosen && request.Agent != "" && len(request.Runs) == 0 && board.Agents[request.Agent] {
				worker = board.Worker{Agent: request.Agent, On: true}
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
			prompt := board.RunPrompts(p, claimed, s.Command)
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
