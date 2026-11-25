package board

import (
	"fmt"
	"strings"
)

// FileRequest posts a new request on behalf of the person an agent is working
// for (THI-107): the run working on request `from` files it, for example a
// follow-up the person asked for in that thread. The new request is the
// person's own (author of `from`), so it queues and runs like one they
// posted, and `from` links to it as related so both threads show where it came
// from. tag is one or more tag ids or labels separated by commas, or "" /
// "auto" to leave it for the agent that takes the request (TagAuto). Text is the title on its first line and
// the body after it.
func (s Store) FileRequest(project string, from int, session, agent, tag, text string) (Request, error) {
	if !Agents[agent] {
		return Request{}, ErrInvalid
	}
	p, err := s.Project(project)
	if err != nil {
		return Request{}, err
	}
	origin, err := s.Request(project, from)
	if err != nil {
		return Request{}, err
	}
	if session == "" || origin.Session != session || origin.Agent != agent {
		return Request{}, ErrConflict
	}
	var tags []string
	if t := strings.TrimSpace(tag); t != "" && !strings.EqualFold(t, "auto") {
		ok := false
		if tags, ok = TagsFor(p, SplitTags(t)); !ok {
			return Request{}, fmt.Errorf("%w: unknown tag in %q", ErrInvalid, tag)
		}
	}
	title, body, _ := strings.Cut(strings.TrimLeft(text, "\r\n"), "\n")
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	r, err := s.CreateRequestWithAttachments(project, origin.Author, title, tags, body, nil)
	if err != nil {
		return r, err
	}
	if _, err := s.SetLink(project, from, r.Number, LinkRelated, agent, session, agent); err != nil {
		return r, err
	}
	return s.Request(project, r.Number)
}
