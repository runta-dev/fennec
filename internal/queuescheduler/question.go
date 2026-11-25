package queuescheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/board"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Querier calls herdr-core on the machine that runs the sessions; executors
// that have one can type answers into an agent's question (THI-176).
type Querier interface {
	Query(ctx context.Context, method string, params any) (json.RawMessage, error)
}

// statusDetail extracts herdr-core's status kind ("question", "permission")
// and one-line message ("answer: Which color? (Red · Blue)").
func statusDetail(raw json.RawMessage) (kind, msg string) {
	var status struct {
		Kind string `json:"kind"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &status)
	return status.Kind, strings.TrimSpace(status.Msg)
}

// statusQuestion is Claude Code's question summary: "answer: <question> (<a> · <b>)".
var statusQuestion = regexp.MustCompile(`^answer: (.+?)(?: \(([^()]*)\))?$`)

// ParseStatusQuestion reads the first question and its option labels from
// herdr-core's status message; the fallback when the transcript is missing.
func ParseStatusQuestion(msg string) []board.QuestionItem {
	m := statusQuestion.FindStringSubmatch(msg)
	if m == nil {
		return nil
	}
	item := board.QuestionItem{Question: m[1]}
	if m[2] != "" {
		for _, label := range strings.Split(m[2], " · ") {
			if label = strings.TrimSpace(label); label != "" {
				item.Options = append(item.Options, board.QuestionOption{Label: label})
			}
		}
	}
	return []board.QuestionItem{item}
}

// ClaudeProjectDir is where Claude Code keeps the transcripts of sessions run
// in cwd: ~/.claude/projects/<cwd with every non-alphanumeric byte as '-'>.
func ClaudeProjectDir(home, cwd string) string {
	b := []byte(cwd)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return filepath.Join(home, ".claude", "projects", string(b))
}

// transcriptTail is how much of the newest transcript is searched for the
// pending AskUserQuestion; the call is among the last few entries.
const transcriptTail = 1 << 20

// TranscriptQuestion returns the AskUserQuestion call that has no result yet
// in the newest Claude Code transcript for cwd.
func TranscriptQuestion(home, cwd string) (id string, items []board.QuestionItem, ok bool) {
	if home == "" || cwd == "" {
		return "", nil, false
	}
	var newest string
	var at time.Time
	// Claude Code's own config dir, or the one Fennec gives agents it
	// launches with CLAUDE_CONFIG_DIR.
	for _, dir := range []string{ClaudeProjectDir(home, cwd), filepath.Join(home, ".local/fennec/native/claude/projects", filepath.Base(ClaudeProjectDir(home, cwd)))} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			if info, err := e.Info(); err == nil && info.ModTime().After(at) {
				newest, at = filepath.Join(dir, e.Name()), info.ModTime()
			}
		}
	}
	if newest == "" {
		return "", nil, false
	}
	file, err := os.Open(newest)
	if err != nil {
		return "", nil, false
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil && info.Size() > transcriptTail {
		_, _ = file.Seek(info.Size()-transcriptTail, io.SeekStart)
	}
	data, _ := io.ReadAll(file)
	type call struct {
		id    string
		items []board.QuestionItem
	}
	var pending []call
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.Contains(line, []byte("AskUserQuestion")) && !bytes.Contains(line, []byte("tool_result")) {
			continue
		}
		var entry struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		var blocks []struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			Name      string `json:"name"`
			ToolUseID string `json:"tool_use_id"`
			Input     struct {
				Questions []struct {
					Header      string                 `json:"header"`
					Question    string                 `json:"question"`
					MultiSelect bool                   `json:"multiSelect"`
					Options     []board.QuestionOption `json:"options"`
				} `json:"questions"`
			} `json:"input"`
		}
		if json.Unmarshal(entry.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch {
			case b.Type == "tool_use" && b.Name == "AskUserQuestion" && len(b.Input.Questions) > 0:
				c := call{id: b.ID}
				for _, q := range b.Input.Questions {
					c.items = append(c.items, board.QuestionItem{Header: q.Header, Question: q.Question, MultiSelect: q.MultiSelect, Options: q.Options})
				}
				pending = append(pending, c)
			case b.Type == "tool_result":
				for i := range pending {
					if pending[i].id == b.ToolUseID {
						pending = append(pending[:i], pending[i+1:]...)
						break
					}
				}
			}
		}
	}
	if len(pending) == 0 {
		return "", nil, false
	}
	last := pending[len(pending)-1]
	return last.id, last.items, true
}

// sameQuestion reports whether a transcript question is the one herdr-core's
// status message summarizes (it names the first question).
func sameQuestion(items []board.QuestionItem, msg string) bool {
	parsed := ParseStatusQuestion(msg)
	if len(parsed) == 0 || len(items) == 0 {
		return false
	}
	a, b := []rune(parsed[0].Question), []rune(items[0].Question)
	n := min(len(a), len(b), 40)
	return n > 0 && string(a[:n]) == string(b[:n])
}

// QuestionFor describes what a blocked agent is asking: AskUserQuestion's
// questions and options from the Claude Code transcript when it matches the
// status, else what the status message says.
func QuestionFor(home string, s Session) board.Question {
	q := board.Question{ID: s.Msg, App: s.App, Kind: s.Kind, Text: s.Msg}
	if s.Kind != "question" {
		return q
	}
	if s.App == "claude-code" {
		if id, items, ok := TranscriptQuestion(home, s.Cwd); ok && sameQuestion(items, s.Msg) {
			q.ID, q.Items = id, items
			return q
		}
	}
	q.Items = ParseStatusQuestion(s.Msg)
	return q
}

// Key timing while driving a dialog; tests shorten it.
var (
	keyPause    = 300 * time.Millisecond
	unblockWait = 6 * time.Second
)

// TypeAnswer answers Claude Code's dialog in terminal with a. AskUserQuestion:
// a digit picks (or, in a multi-select question, ticks) an option, Tab leaves
// a multi-select question, the option after the last is "Type something.",
// and with several questions or any multi-select one a review screen follows
// whose option 1 submits. A permission prompt: 1 approves, Esc denies. What
// keys cannot express (free text in a multi-select question) is typed as a
// message after Esc closes the dialog.
func TypeAnswer(ctx context.Context, query func(context.Context, string, any) (json.RawMessage, error), terminal string, q board.Question, a board.Answer) error {
	keys := func(k ...string) error {
		_, err := query(ctx, "terminal.keys", map[string]any{"terminal": terminal, "keys": k})
		if err == nil {
			err = pause(ctx, keyPause)
		}
		return err
	}
	typed := func(text string, force bool) error {
		_, err := query(ctx, "terminal.send", map[string]any{"terminal": terminal, "text": text, "force": force})
		if err == nil {
			err = pause(ctx, keyPause)
		}
		return err
	}
	instead := func(text string) error {
		if err := keys("esc"); err != nil {
			return err
		}
		if text == "" {
			return nil
		}
		if err := waitUnblocked(ctx, query, terminal); err != nil {
			return err
		}
		return typed(text, false)
	}
	if q.Kind == "permission" {
		if a.Allow != nil && *a.Allow {
			return keys("1")
		}
		return instead(a.Text)
	}
	if len(q.Items) == 0 {
		return instead(a.Text)
	}
	review := len(q.Items) > 1
	for i, item := range q.Items {
		var picks []int
		if i < len(a.Picks) {
			picks = a.Picks[i]
		}
		review = review || item.MultiSelect
		if len(picks) == 0 && (a.Text == "" || item.MultiSelect || len(item.Options) >= 9) {
			return instead(board.AnswerSummary(q, a))
		}
		if len(picks) > 0 && slicesMax(picks) >= 9 {
			return instead(board.AnswerSummary(q, a))
		}
	}
	for i, item := range q.Items {
		var picks []int
		if i < len(a.Picks) {
			picks = a.Picks[i]
		}
		switch {
		case len(picks) == 0:
			if err := keys(strconv.Itoa(len(item.Options) + 1)); err != nil {
				return err
			}
			if err := typed(a.Text, true); err != nil {
				return err
			}
		case item.MultiSelect:
			for _, k := range picks {
				if err := keys(strconv.Itoa(k + 1)); err != nil {
					return err
				}
			}
			if err := keys("tab"); err != nil {
				return err
			}
		default:
			if err := keys(strconv.Itoa(picks[0] + 1)); err != nil {
				return err
			}
		}
	}
	if review {
		return keys("1")
	}
	return nil
}

func slicesMax(v []int) int {
	m := v[0]
	for _, x := range v[1:] {
		m = max(m, x)
	}
	return m
}

func pause(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// waitUnblocked waits for a dismissed dialog to close, so the message that
// follows is a new turn and not an answer to it.
func waitUnblocked(ctx context.Context, query func(context.Context, string, any) (json.RawMessage, error), terminal string) error {
	deadline := time.Now().Add(unblockWait)
	for {
		raw, err := query(ctx, "terminal.get", map[string]any{"terminal": terminal})
		if err != nil {
			return err
		}
		var t struct {
			State  string `json:"state"`
			Status *struct {
				State string `json:"state"`
			} `json:"status"`
		}
		if json.Unmarshal(raw, &t) != nil {
			return fmt.Errorf("invalid terminal state")
		}
		if t.State != "live" {
			return fmt.Errorf("the session ended")
		}
		if t.Status == nil || t.Status.State != "blocked" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the dialog did not close")
		}
		if err = pause(ctx, keyPause); err != nil {
			return err
		}
	}
}

// askingStep keeps an asking run's question current and types its answer
// (THI-176). The session is live and its agent blocked.
func (s Scheduler) askingStep(ctx context.Context, project string, r board.Request, session Session) {
	q := QuestionFor(s.Store.Home, session)
	if r.Question == nil || r.Question.ID != q.ID || r.Question.Text != q.Text || len(r.Question.Items) < len(q.Items) {
		updated, err := s.Store.SetQuestion(project, r.Number, r.Session, q)
		if err != nil {
			s.report(err)
			return
		}
		r = updated
	}
	querier, ok := s.Executor.(Querier)
	if !ok || r.Answer == nil || r.Answer.Sent || r.Question == nil || r.Answer.Question != r.Question.ID || !r.Question.Answerable() {
		return
	}
	if _, err := s.Store.AnswerSent(project, r.Number, r.Answer.UID); err != nil {
		s.report(err)
		return
	}
	attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.report(TypeAnswer(attempt, querier.Query, session.Address, *r.Question, *r.Answer))
}
