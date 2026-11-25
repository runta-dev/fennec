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

// transcriptTail is how much of the newest transcript is searched for a
// pending tool call; the call is among the last few entries.
const transcriptTail = 1 << 20

// pendingCall is a tool call in a Claude Code transcript with no result yet.
// msg is the assistant message it belongs to (Claude Code writes each block
// of a message as its own line under the same message id).
type pendingCall struct {
	id, name, msg string
	input         json.RawMessage
}

// pendingCalls returns the tool calls without a result in the newest Claude
// Code transcript for cwd, oldest first.
func pendingCalls(home, cwd string) []pendingCall {
	if home == "" || cwd == "" {
		return nil
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
		return nil
	}
	file, err := os.Open(newest)
	if err != nil {
		return nil
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil && info.Size() > transcriptTail {
		_, _ = file.Seek(info.Size()-transcriptTail, io.SeekStart)
	}
	data, _ := io.ReadAll(file)
	var pending []pendingCall
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.Contains(line, []byte("tool_")) {
			continue
		}
		var entry struct {
			Message struct {
				ID      string          `json:"id"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		var blocks []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			ToolUseID string          `json:"tool_use_id"`
			Input     json.RawMessage `json:"input"`
		}
		if json.Unmarshal(entry.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				pending = append(pending, pendingCall{id: b.ID, name: b.Name, msg: entry.Message.ID, input: b.Input})
			case "tool_result":
				for i := range pending {
					if pending[i].id == b.ToolUseID {
						pending = append(pending[:i], pending[i+1:]...)
						break
					}
				}
			}
		}
	}
	return pending
}

// TranscriptQuestion returns the AskUserQuestion call that has no result yet
// in the newest Claude Code transcript for cwd.
func TranscriptQuestion(home, cwd string) (id string, items []board.QuestionItem, ok bool) {
	calls := pendingCalls(home, cwd)
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].name != "AskUserQuestion" {
			continue
		}
		var input struct {
			Questions []struct {
				Header      string                 `json:"header"`
				Question    string                 `json:"question"`
				MultiSelect bool                   `json:"multiSelect"`
				Options     []board.QuestionOption `json:"options"`
			} `json:"questions"`
		}
		if json.Unmarshal(calls[i].input, &input) != nil || len(input.Questions) == 0 {
			continue
		}
		for _, q := range input.Questions {
			items = append(items, board.QuestionItem{Header: q.Header, Question: q.Question, MultiSelect: q.MultiSelect, Options: q.Options})
		}
		return calls[i].id, items, true
	}
	return "", nil, false
}

// TranscriptPermission returns the tool call a Claude Code permission prompt
// is about, as "approve <Tool>: <what it runs or touches>" (the shape of
// herdr-core's status message when it names the call). Calls of one message
// are asked about in order, so it is the first call without a result in the
// newest message that still has one.
func TranscriptPermission(home, cwd string) (id, text string, ok bool) {
	calls := pendingCalls(home, cwd)
	if len(calls) == 0 {
		return "", "", false
	}
	last := calls[len(calls)-1].msg
	call := calls[len(calls)-1]
	for _, c := range calls {
		if c.msg == last && last != "" {
			call = c
			break
		}
	}
	if call.name == "" || call.name == "AskUserQuestion" {
		return "", "", false
	}
	text = "approve " + call.name
	if what := callSubject(call.input); what != "" {
		text += ": " + what
	}
	return call.id, text, true
}

// callSubject is the part of a tool call's input a person approves: the
// shell command, the file, the URL, the search.
func callSubject(input json.RawMessage) string {
	var fields map[string]any
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}
	for _, key := range []string{"command", "file_path", "notebook_path", "url", "pattern", "query", "path", "prompt"} {
		if v, ok := fields[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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

// samePermission reports whether a transcript call can be the one a
// permission status message names: any call when the message doesn't name
// one, else the same tool.
func samePermission(text, msg string) bool {
	named, ok := strings.CutPrefix(msg, "approve ")
	if !ok {
		return true
	}
	tool, _, _ := strings.Cut(named, ":")
	call, _, _ := strings.Cut(strings.TrimPrefix(text, "approve "), ":")
	return strings.TrimSpace(tool) == call
}

// QuestionFor describes what a blocked agent is asking: AskUserQuestion's
// questions and options from the Claude Code transcript when it matches the
// status, else what the status message says.
func QuestionFor(home string, s Session) board.Question {
	q := board.Question{ID: s.Msg, App: s.App, Kind: s.Kind, Text: s.Msg}
	if s.Kind == "permission" {
		// THI-205: Claude Code's status can say only "permission prompt";
		// the transcript names the call (and gives each prompt its own id,
		// so an answer to the last prompt doesn't count for the next one).
		if s.App == "claude-code" && s.Msg != "trust this folder" {
			if id, text, ok := TranscriptPermission(home, s.Cwd); ok && samePermission(text, s.Msg) {
				q.ID, q.Text = id, text
			}
		}
		return q
	}
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
