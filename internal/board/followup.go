package board

// Message UIDs survive concurrent replica merges, unlike display row numbers.
func (r Request) HasPendingFollowUp() bool {
	return len(r.PendingFollowUps()) > 0
}

// QueuePendingFollowUps handles a user reply replicated after the agent's
// completion. A working/uncertain execution continues owning its slot.
func (s Store) QueuePendingFollowUps() error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	projects, err := s.Projects()
	if err != nil {
		return err
	}
	for _, p := range projects {
		list, err := s.Requests(p.ID)
		if err != nil {
			return err
		}
		for _, item := range list {
			if item.Status != NeedsYou && item.Status != Review && item.Status != Failed && item.Status != Resolved {
				continue
			}
			r, err := s.Request(p.ID, item.Number)
			if err != nil {
				return err
			}
			if !r.HasPendingFollowUp() {
				continue
			}
			r.Status, r.QueuedAt, r.Updated, r.Offer = Queued, s.now(), s.now(), nil
			if err = s.commit(s.requestPath(p.ID, r.Number), r); err != nil {
				return err
			}
		}
	}
	return nil
}

// PendingFollowUps are the person's messages that no run has been given yet.
func (r Request) PendingFollowUps() []Message {
	seen := map[string]bool{}
	for _, run := range r.Runs {
		for _, id := range run.InputUIDs {
			seen[id] = true
		}
	}
	out := []Message{}
	for _, m := range r.Messages {
		if m.FollowUp && !seen[m.UID] {
			out = append(out, m)
		}
	}
	return out
}

// Steer records that messages the person sent while the agent was working
// were typed into its running session (FEN-169), so they belong to the
// current run: its reply answers them and they are not queued again.
func (s Store) Steer(project string, n int, session string, uids []string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if r.Status != Working || r.Session != session || len(r.Runs) == 0 || r.Runs[len(r.Runs)-1].State != "running" {
		return r, ErrConflict
	}
	run := &r.Runs[len(r.Runs)-1]
	given := map[string]bool{}
	for _, id := range run.InputUIDs {
		given[id] = true
	}
	want := map[string]bool{}
	for _, id := range uids {
		want[id] = true
		if !given[id] {
			run.InputUIDs = append(run.InputUIDs, id)
			given[id] = true
		}
	}
	for _, m := range r.Messages {
		if want[m.UID] && m.ID > r.Delivered {
			r.Delivered = m.ID
		}
	}
	r.Updated = s.now()
	return r, s.commit(s.requestPath(project, n), r)
}
