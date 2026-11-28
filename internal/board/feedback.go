package board

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Feedback (FEN-217, FEN-222) is a message from someone outside the project:
// a player on Discord, a visitor using the web widget, and so on. It is an
// ordinary message of a request thread whose author is External. A new piece
// of Feedback opens a request tagged Feedback plus its channel (Discord,
// Widget, Slack, X); more Feedback about the same thing joins that thread.
// Messages without External and without Agent are the project's own people,
// whom clients show with an Owner badge.

// FeedbackTag is the tag every request opened by Feedback gets.
var FeedbackTag = Tag{ID: "feedback", Label: "Feedback"}

// Channels are where Feedback can come from, by id, with their tag label.
var Channels = map[string]string{"discord": "Discord", "widget": "Widget", "slack": "Slack", "x": "X"}

// External is the author of a message from outside the project.
type External struct {
	// Name is what the channel calls them ("Edward", "Anonymous #a91f").
	Name string `json:"name"`
	// Channel is a Channels id.
	Channel string `json:"channel"`
	// Context is what the channel knew about their setup.
	Context *FeedbackContext `json:"context,omitempty"`
	// ReplyRef is the channel's own handle for answering them. It is opaque:
	// only the channel adapter reads it, and agents never see it.
	ReplyRef string `json:"reply_ref,omitempty"`
}

// FeedbackContext is the app and device the Feedback was sent from.
type FeedbackContext struct {
	Version string `json:"version,omitempty"`
	Device  string `json:"device,omitempty"`
	OS      string `json:"os,omitempty"`
}

const maxReplyRef = 1024

func validLine(text string, limit int) bool {
	return validText(text, limit) && !strings.ContainsAny(text, "\r\n")
}

func optionalLine(text string, limit int) bool {
	return text == "" || validLine(text, limit)
}

// Valid reports whether e names someone on a known channel.
func (e External) Valid() bool {
	if _, ok := Channels[e.Channel]; !ok || !validLine(e.Name, 80) {
		return false
	}
	if c := e.Context; c != nil && !(optionalLine(c.Version, 80) && optionalLine(c.Device, 80) && optionalLine(c.OS, 80)) {
		return false
	}
	return len(e.ReplyRef) <= maxReplyRef && utf8.ValidString(e.ReplyRef) && !strings.ContainsRune(e.ReplyRef, 0)
}

// Details is the context as one line: "iPhone 11 · iOS 18.2 · 1.4.1".
func (c *FeedbackContext) Details() string {
	if c == nil {
		return ""
	}
	parts := []string{}
	for _, part := range []string{c.Device, c.OS, c.Version} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " · ")
}

// Who is how an agent sees the author of m: "Edward (external user via
// Discord; iPhone 11 · 1.4.1)", "claude (agent)" or the person's name.
func (m Message) Who() string {
	switch {
	case m.External != nil:
		who := m.External.Name + " (external user via " + Channels[m.External.Channel]
		if details := m.External.Context.Details(); details != "" {
			who += "; " + details
		}
		return who + ")"
	case m.Agent != "":
		return m.Agent + " (agent)"
	}
	return m.Author
}

// ensureFeedbackTags adds the Feedback tag and the channel's tag to p when it
// lacks them, and returns their ids.
func ensureFeedbackTags(p *Project, channel string) []string {
	ids := []string{}
	for _, want := range []Tag{FeedbackTag, {ID: channel, Label: Channels[channel]}} {
		id, ok := TagFor(*p, want.Label)
		if !ok {
			p.Tags = append(p.Tags, want)
			id = want.ID
		}
		ids = append(ids, id)
	}
	return ids
}

// PostFeedback opens a request for new Feedback, tagged Feedback plus its
// channel. It waits as Queued for the project's agent like any request.
func (s Store) PostFeedback(project string, from External, title, body string) (Request, error) {
	if !from.Valid() {
		return Request{}, ErrInvalid
	}
	return s.createRequest(project, from.Name, title, nil, body, nil, Choice{}, &from)
}

// AddFeedback adds Feedback to an existing request's thread, e.g. a second
// player hitting the same crash. Like a person's reply it does not schedule
// work; the request also gets the channel's tag.
func (s Store) AddFeedback(project string, n int, from External, body string) (Request, error) {
	if !from.Valid() || !validText(body, maxBody) {
		return Request{}, ErrInvalid
	}
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
	if len(r.Messages) >= 2000 {
		return r, ErrInvalid
	}
	before := len(p.Tags)
	tags, _ := TagsFor(p, append(r.TagList(), ensureFeedbackTags(&p, from.Channel)...))
	now := s.now()
	id := 1
	if len(r.Messages) > 0 {
		id = r.Messages[len(r.Messages)-1].ID + 1
	}
	r.Messages = append(r.Messages, Message{UID: randomID(), ID: id, Author: from.Name, External: &from, Body: body, At: now})
	r.setTags(tags)
	r.Updated = now
	if len(p.Tags) != before {
		if err := s.commit(filepath.Join(s.projectDir(project), "project.json"), p); err != nil {
			return r, err
		}
	}
	return r, s.commit(s.requestPath(project, n), r)
}
