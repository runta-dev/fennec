package board

// Message UIDs survive concurrent replica merges, unlike display row numbers.
func (r Request) HasPendingFollowUp() bool {
	seen := map[string]bool{}
	for _, run := range r.Runs {
		for _, id := range run.InputUIDs {
			seen[id] = true
		}
	}
	for _, m := range r.Messages {
		if m.FollowUp && !seen[m.UID] {
			return true
		}
	}
	return false
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
