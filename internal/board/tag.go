package board

import (
	"fmt"
	"strings"
)

// A request posted without a tag gets the project's first tag as a
// placeholder (TagAuto). The agent working on it has read the request, so it
// tags it: `fennec-terminal queue tag`, which calls AutoTag. A person who
// picks a tag clears TagAuto, and agents leave their choice alone.

// TagFor resolves what an agent typed, a tag id or its label in any case, to
// one of the project's tag ids.
func TagFor(p Project, name string) (string, bool) {
	name = strings.TrimSpace(name)
	for _, t := range p.Tags {
		if t.ID == name {
			return t.ID, true
		}
	}
	for _, t := range p.Tags {
		if strings.EqualFold(t.ID, name) || strings.EqualFold(strings.TrimSpace(t.Label), name) {
			return t.ID, true
		}
	}
	return "", false
}

// AutoTag sets the tag of a request whose tag nobody chose. Only the run
// working on it (its session and agent) may; a tag a person picked is a
// conflict. Tagging is not activity, so Updated stays.
func (s Store) AutoTag(project string, n int, session, agent, name string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	p, err := s.Project(project)
	if err != nil {
		return Request{}, err
	}
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	tag, ok := TagFor(p, name)
	if !ok {
		return r, ErrInvalid
	}
	if session == "" || r.Session != session || r.Agent != agent || !r.TagAuto {
		return r, ErrConflict
	}
	if r.Tag == tag {
		return r, nil
	}
	r.Tag = tag
	return r, s.commit(s.requestPath(project, n), r)
}

// tagPrompt asks the agent to tag a request whose tag nobody chose, naming
// the project's tags and the command that sets one.
func tagPrompt(p Project, r Request, command string) string {
	if !r.TagAuto {
		return ""
	}
	tags := make([]string, 0, len(p.Tags))
	for _, t := range p.Tags {
		tags = append(tags, fmt.Sprintf("%s (%s)", t.ID, t.Label))
	}
	return fmt.Sprintf("Nobody chose this request's tag yet. Once you understand it, set the one that fits best (%s) with:\n%s tag %s %d %s %s <tag>\n",
		strings.Join(tags, ", "), command, p.ID, r.Number, r.Agent, r.Session)
}
