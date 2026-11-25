package board

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type SyncReport struct {
	Seen          map[string]uint64 `json:"seen"`
	At            time.Time         `json:"at"`
	PendingImages bool              `json:"pending_images,omitempty"`
}

func (s Store) RecordSync(seen map[string]uint64) error {
	return s.RecordImageSync(seen, false)
}
func (s Store) RecordImageSync(seen map[string]uint64, pending bool) error {
	return writeJSON(filepath.Join(s.root(), "sync-status.json"), SyncReport{Seen: seen, At: s.now(), PendingImages: pending})
}

// SyncedAt is when the Mac replica last completed a sync with the cloud board;
// zero on the cloud (it is the source) or before the first sync.
func (s Store) SyncedAt() time.Time {
	var report SyncReport
	if s.Role == "cloud" || readJSON(filepath.Join(s.root(), "sync-status.json"), &report) != nil {
		return time.Time{}
	}
	return report.At
}

func (s Store) SyncStatus() string {
	if s.Role == "cloud" {
		return "synced"
	}
	var report SyncReport
	if readJSON(filepath.Join(s.root(), "sync-status.json"), &report) != nil || s.now().Sub(report.At) > 15*time.Second {
		return "offline"
	}
	state, err := s.replica()
	if err != nil {
		return "offline"
	}
	if report.PendingImages || state.Seen[state.ID] > report.Seen[state.ID] {
		return "pending"
	}
	return "synced"
}

// Slot ownership is transferred by its current owner only. A transferred slot
// is unusable at the sender immediately, and usable at the receiver only after
// replication. Lost acknowledgements cannot create additional capacity.
type Slot struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
	Owner string `json:"owner"`
}
type Offer struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	Epoch uint64 `json:"epoch"`
}
type Routing struct {
	Locations map[string]string `json:"locations"`
	Device    string            `json:"device"`
}
type LocalControl struct {
	Epoch   uint64 `json:"epoch"`
	Primary bool   `json:"primary"`
}

func (s Store) Routing() Routing {
	var r Routing
	_ = readJSON(filepath.Join(s.root(), "routing.json"), &r)
	if r.Locations == nil {
		r.Locations = map[string]string{}
	}
	return r
}
func (s Store) LocalControl() LocalControl {
	var c LocalControl
	_ = readJSON(filepath.Join(s.root(), "local-control.json"), &c)
	return c
}
func (s Store) SetRouting(r Routing) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.commit(filepath.Join(s.root(), "routing.json"), r)
}
func (s Store) SetPrimary(primary bool) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	c := s.LocalControl()
	if c.Epoch > 0 && c.Primary == primary {
		return nil
	}
	c.Epoch++
	c.Primary = primary
	return s.commit(filepath.Join(s.root(), "local-control.json"), c)
}
func (s Store) TransferIdle(project, to string) error {
	if to == s.Role || (to != "local" && to != "cloud") {
		return ErrInvalid
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	list, err := s.Requests(project)
	if err != nil {
		return err
	}
	used := 0
	for _, r := range list {
		if r.RunLocation == s.Role && (r.Status == Working || r.Status == Unknown) {
			used++
		}
	}
	owned := 0
	for _, slot := range p.Slots {
		if slot.Owner == s.Role && slot.Index < p.Queue {
			owned++
		}
	}
	if owned <= used {
		return ErrConflict
	}
	for i := len(p.Slots) - 1; i >= 0; i-- {
		if p.Slots[i].Owner == s.Role && p.Slots[i].Index < p.Queue {
			p.Slots[i].Owner = to
			return s.commit(filepath.Join(s.projectDir(project), "project.json"), p)
		}
	}
	return ErrConflict
}
func (s Store) Propose(project string, n int, agent string, epoch uint64) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return err
	}
	if r.Status != Queued {
		return ErrConflict
	}
	if r.Offer != nil && r.Offer.Epoch == epoch && r.Offer.Agent == agent {
		return nil
	}
	r.Offer = &Offer{ID: randomID(), Agent: agent, Epoch: epoch}
	return s.commit(s.requestPath(project, n), r)
}
func (s Store) validateDispatch(p Project, list []Summary, n int, agent, location, session string) error {
	if s.Role == "" {
		return nil
	}
	if location != s.Role {
		return ErrConflict
	}
	owned, used := 0, 0
	for _, slot := range p.Slots {
		if slot.Owner == location && slot.Index < p.Queue {
			owned++
		}
	}
	for _, r := range list {
		if r.RunLocation == location && (r.Status == Working || r.Status == Unknown) {
			used++
		}
	}
	if used >= owned {
		return ErrConflict
	}
	if location == "local" {
		c := s.LocalControl()
		if c.Primary {
			r, err := s.Request(p.ID, n)
			if err != nil {
				return err
			}
			if r.Offer == nil || r.Offer.Epoch != c.Epoch || r.Offer.Agent != agent || r.Offer.ID != session {
				return ErrConflict
			}
		}
	}
	return nil
}

// RunPrompt is what an agent working on a queue request is asked to do. It
// names only the root of the queue's OKF knowledge bundle; the agent decides
// what goes in it and how it is arranged.
func RunPrompt(p Project, r Request, command string) string {
	return fmt.Sprintf("Handle request %s-%d in queue %s.\nQueue instructions: %s\nRepository (optional): %s\n\n%s\n\n%s\n\n%s\n\n%sReport your final result on stdin using:\n%s reply %s %d %s %s resolved\nOutcome resolved hands finished work to the user to review and mark done; use you for a question, or failed for a failure. These commands release the execution slot; do not use them for progress updates. End every turn with this command unless a background command or monitor you actually started will wake you; saying you will continue later keeps nothing running, and the request soon shows as stopped without reporting back.\nRead this request with: %s read %s %d\n", p.Key, r.Number, p.Name, p.Description, p.Repository, Thread(p, r), knowledgeNote(p), threadsNote(p, r, command), tagPrompt(p, r, command), command, p.ID, r.Number, r.Agent, r.Session, command, p.ID, r.Number)
}

func knowledgeNote(p Project) string {
	return fmt.Sprintf("Queue knowledge: ~/.fen/board/projects/%s/knowledge/ is this queue's OKF bundle (https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md); people read it in Fennec. Read what is relevant there before you start. You maintain it: while you work, record what a later agent on this queue should know, and whenever the user asks you to remember something, record that too. Update or deprecate entries that turn out wrong instead of adding duplicates.", p.ID)
}

// threadsNote points the agent at the queue's mirrored threads (THI-103) and
// the commands that link this request to them.
func threadsNote(p Project, r Request, command string) string {
	return fmt.Sprintf("Queue threads: every request of this queue is mirrored as markdown in ~/.fen/board/projects/%s/threads/ (INDEX.md lists them, %s-<n>.md holds each thread with its links). Search it (rg, grep) for earlier requests about the same thing and use what they found. Mentioning %s-<n> in your reply links the two threads; to link explicitly run:\n%s link %s %d %s %s %s-<n> [related|duplicate|none]\nIf this request repeats an earlier one, merge it into that one with:\n%s merge %s %d %s %s %s-<n>\nTo file a new request in this queue for the person (work they ask you to queue, or a follow-up you both agreed on), pass its title on the first line and its body after it on stdin, and name one of %s, or auto:\n%s new %s %d %s %s <tag|auto>\nIt is theirs, starts as Queued, and links to this request.", p.ID, p.Key, p.Key, command, p.ID, r.Number, r.Agent, r.Session, p.Key, command, p.ID, r.Number, r.Agent, r.Session, p.Key, tagIDs(p), command, p.ID, r.Number, r.Agent, r.Session)
}

// tagIDs lists a project's tag ids for the run prompt.
func tagIDs(p Project) string {
	ids := make([]string, 0, len(p.Tags))
	for _, t := range p.Tags {
		ids = append(ids, t.ID)
	}
	return strings.Join(ids, ", ")
}
