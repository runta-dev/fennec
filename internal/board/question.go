package board

import (
	"strings"
	"time"
)

// Question is what a request's agent is asking in its session while its run is
// "asking" (THI-176): Claude Code's AskUserQuestion or a permission prompt. The
// scheduler that runs the session records it; people answer it from the thread
// (Store.Answer) and that scheduler types the answer into the session.
type Question struct {
	// ID tells one question from the next: the AskUserQuestion tool_use id,
	// else herdr-core's status message.
	ID string `json:"id"`
	// App is the agent that asked (herdr-core status app, e.g. "claude-code");
	// only agents whose dialogs the scheduler knows how to drive are answerable.
	App string `json:"app,omitempty"`
	// Kind is herdr-core's status kind: "question" or "permission".
	Kind string `json:"kind,omitempty"`
	// Text is herdr-core's one-line status message, e.g. "answer: Which color?
	// (Red · Blue)" or "approve Bash: rm -rf build".
	Text  string         `json:"text,omitempty"`
	Items []QuestionItem `json:"items,omitempty"`
	At    time.Time      `json:"at"`
}

// QuestionItem is one question of an AskUserQuestion call.
type QuestionItem struct {
	Header      string           `json:"header,omitempty"`
	Question    string           `json:"question"`
	MultiSelect bool             `json:"multi_select,omitempty"`
	Options     []QuestionOption `json:"options,omitempty"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Answer is a person's answer to Question, waiting for (or past) delivery
// into the agent's session.
type Answer struct {
	UID string `json:"uid"`
	// Question is the Question.ID this answers; a newer question discards it.
	Question string `json:"question"`
	// Picks holds, per question item, the chosen option indexes (zero-based).
	Picks [][]int `json:"picks,omitempty"`
	// Allow answers a permission prompt: true approves, false denies.
	Allow *bool `json:"allow,omitempty"`
	// Text is a typed answer: the "Type something" choice of every question
	// item without a pick, or what to do instead of a denied permission.
	Text   string    `json:"text,omitempty"`
	Author string    `json:"author"`
	At     time.Time `json:"at"`
	// Sent is set once the scheduler has started typing it; it is never typed twice.
	Sent bool `json:"sent,omitempty"`
}

// Answerable reports whether the scheduler can type an answer to q.
func (q *Question) Answerable() bool {
	return q != nil && q.App == "claude-code" && (q.Kind == "question" || q.Kind == "permission")
}

// SetQuestion records what an asking run's agent is asking. A different
// question replaces the previous one and drops an answer given to it.
func (s Store) SetQuestion(project string, n int, session string, q Question) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if r.Session != session || !r.Asking() {
		return r, ErrConflict
	}
	if r.Question != nil && r.Question.ID == q.ID && r.Question.Text == q.Text && len(r.Question.Items) >= len(q.Items) {
		return r, nil
	}
	if q.At.IsZero() {
		q.At = s.now()
	}
	r.Question = &q
	if r.Answer != nil && r.Answer.Question != q.ID {
		r.Answer = nil
	}
	return r, s.commit(s.requestPath(project, n), r)
}

// Answer stores a person's answer to the question an asking run's agent is
// waiting on and adds it to the thread. It does not start a new run: the
// scheduler running the session types it there (see AnswerSent).
func (s Store) Answer(project string, n int, a Answer) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	a.Text = strings.TrimSpace(a.Text)
	if !validText(a.Author, 80) || (a.Text != "" && !validText(a.Text, maxBody)) || len(r.Messages) >= 2000 {
		return r, ErrInvalid
	}
	q := r.Question
	if !r.Asking() || !q.Answerable() || (a.Question != "" && a.Question != q.ID) || r.Answer != nil {
		return r, ErrConflict
	}
	a.Question = q.ID
	switch q.Kind {
	case "permission":
		a.Picks = nil
		if a.Allow == nil {
			deny := false
			a.Allow = &deny
		}
		if *a.Allow {
			a.Text = ""
		}
	default:
		a.Allow = nil
		if len(a.Picks) > len(q.Items) {
			return r, ErrInvalid
		}
		for i, picks := range a.Picks {
			if len(picks) > 1 && !q.Items[i].MultiSelect {
				return r, ErrInvalid
			}
			seen := map[int]bool{}
			for _, k := range picks {
				if k < 0 || k >= len(q.Items[i].Options) || seen[k] {
					return r, ErrInvalid
				}
				seen[k] = true
			}
		}
		// Every question needs a pick, or the typed answer stands for it.
		if a.Text == "" {
			if len(q.Items) == 0 {
				return r, ErrInvalid
			}
			for i := range q.Items {
				if i >= len(a.Picks) || len(a.Picks[i]) == 0 {
					return r, ErrInvalid
				}
			}
		}
	}
	now := s.now()
	a.UID, a.At, a.Sent = randomID(), now, false
	r.Answer = &a
	id := 1
	if len(r.Messages) > 0 {
		id = r.Messages[len(r.Messages)-1].ID + 1
	}
	m := Message{UID: randomID(), ID: id, Author: a.Author, Body: AnswerSummary(*q, a), At: now}
	r.Messages = append(r.Messages, m)
	// The answer reaches the agent through its dialog, not as the next
	// run's input.
	run := &r.Runs[len(r.Runs)-1]
	run.InputUIDs = append(run.InputUIDs, m.UID)
	r.Delivered, r.Updated = id, now
	return r, s.commit(s.requestPath(project, n), r)
}

// AnswerSent marks an answer as typed into the session, before it is typed:
// a lost receipt must never answer a later question with it.
func (s Store) AnswerSent(project string, n int, uid string) (Request, error) {
	unlock, err := s.lock()
	if err != nil {
		return Request{}, err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return r, err
	}
	if !r.Asking() || r.Answer == nil || r.Answer.UID != uid || r.Answer.Sent {
		return r, ErrConflict
	}
	r.Answer.Sent = true
	return r, s.commit(s.requestPath(project, n), r)
}

// AnswerSummary is the thread message for an answer, in the agent's words for
// the choices (no UI language: the thread is read by people and agents alike).
func AnswerSummary(q Question, a Answer) string {
	if q.Kind == "permission" {
		cmd := strings.TrimPrefix(q.Text, "approve ")
		switch {
		case a.Allow != nil && *a.Allow:
			return "✅ " + cmd
		case a.Text != "":
			return "⛔ " + cmd + "\n\n" + a.Text
		default:
			return "⛔ " + cmd
		}
	}
	lines := []string{}
	for i, item := range q.Items {
		chosen := []string{}
		if i < len(a.Picks) {
			for _, k := range a.Picks[i] {
				chosen = append(chosen, item.Options[k].Label)
			}
		}
		if len(chosen) == 0 && a.Text != "" {
			chosen = append(chosen, a.Text)
		}
		if len(chosen) == 0 {
			continue
		}
		lines = append(lines, "> "+item.Question+"\n\n"+strings.Join(chosen, ", "))
	}
	if len(lines) == 0 {
		return a.Text
	}
	return strings.Join(lines, "\n\n")
}

// clearQuestion drops a question once its run is no longer asking.
func clearQuestion(r *Request) {
	r.Question, r.Answer = nil, nil
}
