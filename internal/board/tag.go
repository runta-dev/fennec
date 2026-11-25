package board

import (
	"fmt"
	"slices"
	"strings"
)

// A request posted without a tag gets the project's first tag as a
// placeholder (TagAuto). The agent working on it has read the request, so it
// tags it: `fennec-terminal queue tag`, which calls AutoTag. A person who
// picks tags clears TagAuto, and agents leave their choice alone.
//
// A request can have several tags (THI-133), say Bug and Desktop. Tags holds
// them all; Tag stays the first one, so builds that know only Tag still show
// and filter by it.

// TagList is the request's tags, Tag first. A request saved before THI-133,
// or whose Tag a build without Tags changed since, has just its Tag.
func (r Request) TagList() []string {
	if len(r.Tags) == 0 || r.Tags[0] != r.Tag {
		return []string{r.Tag}
	}
	return r.Tags
}

// setTags stores tags (non-empty project tag ids), keeping Tag the first and
// Tags empty while there is only one, as before THI-133.
func (r *Request) setTags(tags []string) {
	r.Tag, r.Tags = tags[0], nil
	if len(tags) > 1 {
		r.Tags = tags
	}
}

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

// TagsFor resolves tag ids or labels to the project's tag ids, once each and
// in the project's tag order, so a kind (Bug) comes before an area (Desktop).
// It fails on an unknown tag or none at all.
func TagsFor(p Project, names []string) ([]string, bool) {
	picked := map[string]bool{}
	for _, name := range names {
		id, ok := TagFor(p, name)
		if !ok {
			return nil, false
		}
		picked[id] = true
	}
	ids := []string{}
	for _, t := range p.Tags {
		if picked[t.ID] {
			ids = append(ids, t.ID)
		}
	}
	return ids, len(ids) > 0
}

// SplitTags splits what an agent typed for tags: "bug,desktop", "bug desktop"
// or one tag.
func SplitTags(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
}

// AutoTag sets the tags of a request whose tag nobody chose. Only the run
// working on it (its session and agent) may; tags a person picked are a
// conflict. Tagging is not activity, so Updated stays. names is one or more
// tag ids or labels, separated by commas.
func (s Store) AutoTag(project string, n int, session, agent, names string) (Request, error) {
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
	tags, ok := TagsFor(p, SplitTags(names))
	if !ok {
		return r, ErrInvalid
	}
	if session == "" || r.Session != session || r.Agent != agent || !r.TagAuto {
		return r, ErrConflict
	}
	if slices.Equal(r.TagList(), tags) {
		return r, nil
	}
	r.setTags(tags)
	return r, s.commit(s.requestPath(project, n), r)
}

// TagLabels is the request's tags by label, for text an agent or a person
// reads.
func TagLabels(p Project, r Request) string {
	labels := []string{}
	for _, id := range r.TagList() {
		label := id
		for _, t := range p.Tags {
			if t.ID == id {
				label = t.Label
			}
		}
		labels = append(labels, label)
	}
	return strings.Join(labels, ", ")
}

// tagPrompt asks the agent to tag a request whose tag nobody chose, naming
// the project's tags and the command that sets them.
func tagPrompt(p Project, r Request, command string) string {
	if !r.TagAuto {
		return ""
	}
	tags := make([]string, 0, len(p.Tags))
	for _, t := range p.Tags {
		tags = append(tags, fmt.Sprintf("%s (%s)", t.ID, t.Label))
	}
	return fmt.Sprintf("Nobody chose this request's tags yet. Once you understand it, set every tag that fits, for example its kind and the parts it touches (%s), separated by commas, with:\n%s tag %s %d %s %s <tag>[,<tag>...]\n",
		strings.Join(tags, ", "), command, p.ID, r.Number, r.Agent, r.Session)
}
