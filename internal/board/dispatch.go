package board

import (
	"fmt"
	"path/filepath"
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
func RunPrompt(p Project, r Request, command string) string {
	return fmt.Sprintf("Handle request %s-%d in queue %s.\nQueue instructions: %s\nRepository (optional): %s\n\n%s\n\nReport your final result on stdin using:\n%s reply %s %d %s %s resolved\nUse outcome you for a question/review, or failed for a failure. These commands release the execution slot; do not use them for progress updates.\nRead this request with: %s read %s %d\n", p.Key, r.Number, p.Name, p.Description, p.Repository, Thread(p, r), command, p.ID, r.Number, r.Agent, r.Session, command, p.ID, r.Number)
}
