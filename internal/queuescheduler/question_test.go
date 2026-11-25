package queuescheduler

import (
	"context"
	"encoding/json"
	"fennec/internal/board"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// typingExecutor records what the scheduler types into sessions.
type typingExecutor struct {
	*executorFixture
	calls []string
	// blocked answers terminal.get with a blocked status this many times.
	blocked int
}

func (e *typingExecutor) Query(_ context.Context, method string, params any) (json.RawMessage, error) {
	p := params.(map[string]any)
	switch method {
	case "terminal.keys":
		e.calls = append(e.calls, "keys "+strings.Join(p["keys"].([]string), " "))
	case "terminal.send":
		force := ""
		if p["force"] == true {
			force = " (force)"
		}
		e.calls = append(e.calls, "send "+p["text"].(string)+force)
		return json.RawMessage(`{"started":true}`), nil
	case "terminal.get":
		if e.blocked > 0 {
			e.blocked--
			return json.RawMessage(`{"state":"live","status":{"state":"blocked"}}`), nil
		}
		return json.RawMessage(`{"state":"live","status":{"state":"idle"}}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func fastKeys(t *testing.T) {
	old, oldWait := keyPause, unblockWait
	keyPause, unblockWait = time.Millisecond, time.Second
	t.Cleanup(func() { keyPause, unblockWait = old, oldWait })
}

const askCwd = "/root/.fen/0123456789abcdef0123456789abcdef"

func writeTranscript(t *testing.T, home string, lines ...string) {
	t.Helper()
	dir := ClaudeProjectDir(home, askCwd)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

const colorCall = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_color","name":"AskUserQuestion","input":{"questions":[{"question":"Which color should the button be?","header":"Color","multiSelect":false,"options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"}]}]}}]}}`

func askingAgent(ex *executorFixture, r board.Request, kind, msg string) {
	ex.sessions = []Session{{ID: r.Session, Address: "t1", State: "live", Status: "blocked", App: "claude-code", Kind: kind, Msg: msg, Cwd: askCwd, StatusAt: time.Now()}}
}

func TestClaudeProjectDir(t *testing.T) {
	if got := ClaudeProjectDir("/h", "/root/.fen/452f"); got != "/h/.claude/projects/-root--fen-452f" {
		t.Fatal(got)
	}
	if got := ClaudeProjectDir("/h", "/tmp/claude-0/askprobe"); got != "/h/.claude/projects/-tmp-claude-0-askprobe" {
		t.Fatal(got)
	}
}

func TestParseStatusQuestion(t *testing.T) {
	got := ParseStatusQuestion("answer: Which color should the button be? (Red · Blue)")
	want := []board.QuestionItem{{Question: "Which color should the button be?", Options: []board.QuestionOption{{Label: "Red"}, {Label: "Blue"}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if got := ParseStatusQuestion("trust this folder"); got != nil {
		t.Fatalf("%+v", got)
	}
}

func TestTranscriptQuestionSkipsAnsweredCalls(t *testing.T) {
	home := t.TempDir()
	answered := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_old","name":"AskUserQuestion","input":{"questions":[{"question":"Old?","options":[{"label":"A"}]}]}}]}}`
	result := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_old","content":"answered"}]}}`
	writeTranscript(t, home, answered, result)
	if _, _, ok := TranscriptQuestion(home, askCwd); ok {
		t.Fatal("answered call reported as pending")
	}
	writeTranscript(t, home, answered, result, colorCall)
	id, items, ok := TranscriptQuestion(home, askCwd)
	if !ok || id != "toolu_color" || len(items) != 1 || items[0].Header != "Color" || items[0].Options[1].Description != "Cool" {
		t.Fatalf("%v %q %+v", ok, id, items)
	}
}

// THI-176: the card and thread show what the agent asks; answering from the
// thread types it into the session, and the request runs again without a
// new run.
func TestThreadAnswerIsTypedIntoTheSession(t *testing.T) {
	fastKeys(t)
	s, ex, p, r := runningOnCloud(t)
	te := &typingExecutor{executorFixture: ex}
	s.Executor = te
	writeTranscript(t, s.Store.Home, colorCall)
	askingAgent(ex, r, "question", "answer: Which color should the button be? (Red · Blue)")
	step(t, s)
	got, _ := s.Store.Request(p.ID, r.Number)
	if !got.Asking() || got.Question == nil || got.Question.ID != "toolu_color" || len(got.Question.Items) != 1 || got.Question.Items[0].Options[0].Description != "Warm" {
		t.Fatalf("question not recorded: %+v", got.Question)
	}
	if _, err := s.Store.Answer(p.ID, r.Number, board.Answer{Author: "me", Question: "toolu_color", Picks: [][]int{{1}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Answer(p.ID, r.Number, board.Answer{Author: "me", Picks: [][]int{{0}}}); err != board.ErrConflict {
		t.Fatalf("second answer: %v", err)
	}
	step(t, s)
	if want := []string{"keys 2"}; !reflect.DeepEqual(te.calls, want) {
		t.Fatalf("typed %q, want %q", te.calls, want)
	}
	// Still blocked for a moment: the answer is never typed twice.
	step(t, s)
	if len(te.calls) != 1 {
		t.Fatalf("typed again: %q", te.calls)
	}
	agentIs(ex, r, "working", time.Now())
	step(t, s)
	got, _ = s.Store.Request(p.ID, r.Number)
	if got.Status != board.Working || len(got.Runs) != 1 || ex.starts != 1 || got.Question != nil || got.Answer != nil {
		t.Fatalf("not running on the same run: %+v (%d starts)", got, ex.starts)
	}
	last := got.Messages[len(got.Messages)-1]
	if last.Author != "me" || !strings.Contains(last.Body, "Blue") || got.HasPendingFollowUp() {
		t.Fatalf("answer message %+v", last)
	}
}

func TestQuestionFallsBackToStatusMessage(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	askingAgent(ex, r, "question", "answer: Ship it? (Yes · No)")
	step(t, s)
	got, _ := s.Store.Request(p.ID, r.Number)
	if got.Question == nil || len(got.Question.Items) != 1 || got.Question.Items[0].Question != "Ship it?" || len(got.Question.Items[0].Options) != 2 {
		t.Fatalf("%+v", got.Question)
	}
}

func TestNewQuestionDropsAnUnsentAnswer(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	askingAgent(ex, r, "question", "answer: First? (A · B)")
	step(t, s)
	got, _ := s.Store.Request(p.ID, r.Number)
	// Answered on the Mac, but the session already asks something else.
	if _, err := s.Store.Answer(p.ID, r.Number, board.Answer{Author: "me", Question: got.Question.ID, Picks: [][]int{{0}}}); err != nil {
		t.Fatal(err)
	}
	askingAgent(ex, r, "question", "answer: Second? (C · D)")
	step(t, s)
	got, _ = s.Store.Request(p.ID, r.Number)
	if got.Question.Items[0].Question != "Second?" || got.Answer != nil {
		t.Fatalf("stale answer kept: %+v %+v", got.Question, got.Answer)
	}
}

func TestPermissionPromptIsShownAndAnswered(t *testing.T) {
	fastKeys(t)
	s, ex, p, r := runningOnCloud(t)
	te := &typingExecutor{executorFixture: ex}
	s.Executor = te
	askingAgent(ex, r, "permission", "approve Bash: rm -rf build")
	step(t, s)
	got, _ := s.Store.Request(p.ID, r.Number)
	if got.Question == nil || got.Question.Kind != "permission" || got.Question.Text != "approve Bash: rm -rf build" || !got.Question.Answerable() {
		t.Fatalf("%+v", got.Question)
	}
	allow := true
	if _, err := s.Store.Answer(p.ID, r.Number, board.Answer{Author: "me", Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Store.Request(p.ID, r.Number)
	last := got.Messages[len(got.Messages)-1]
	if last.Permission == nil || !last.Permission.Allow || last.Permission.Command != "Bash: rm -rf build" ||
		last.Body != "Allowed:\n\n```\nBash: rm -rf build\n```" {
		t.Fatalf("answer message %+v %+v", last, last.Permission)
	}
	step(t, s)
	if want := []string{"keys 1"}; !reflect.DeepEqual(te.calls, want) {
		t.Fatalf("typed %q", te.calls)
	}
}

func TestAskingRunWhoseTerminalEndedFails(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	askingAgent(ex, r, "question", "answer: Ship it? (Yes · No)")
	step(t, s)
	ex.sessions[0].State = "finished"
	step(t, s)
	got, _ := s.Store.Request(p.ID, r.Number)
	if got.Status != board.Failed || got.Question != nil || got.Asking() {
		t.Fatalf("ended session still asking: %+v", got)
	}
}

func TestDismissedQuestionClearsIt(t *testing.T) {
	s, ex, p, r := runningOnCloud(t)
	askingAgent(ex, r, "question", "answer: Ship it? (Yes · No)")
	step(t, s)
	agentIs(ex, r, "idle", time.Now())
	step(t, s)
	got, _ := s.Store.Request(p.ID, r.Number)
	if got.Failure != board.StalledFailure || got.Question != nil {
		t.Fatalf("%+v", got)
	}
}

func TestTypeAnswerKeys(t *testing.T) {
	fastKeys(t)
	yes, no := true, false
	single := board.QuestionItem{Question: "Size?", Options: []board.QuestionOption{{Label: "S"}, {Label: "L"}}}
	multi := board.QuestionItem{Question: "Extras?", MultiSelect: true, Options: []board.QuestionOption{{Label: "Cheese"}, {Label: "Ham"}, {Label: "Olives"}}}
	ask := func(items ...board.QuestionItem) board.Question {
		return board.Question{App: "claude-code", Kind: "question", Items: items}
	}
	for _, c := range []struct {
		name    string
		q       board.Question
		a       board.Answer
		blocked int
		want    []string
	}{
		{"one pick", ask(single), board.Answer{Picks: [][]int{{1}}}, 0, []string{"keys 2"}},
		{"two questions", ask(single, multi), board.Answer{Picks: [][]int{{1}, {0, 2}}}, 0, []string{"keys 2", "keys 1", "keys 3", "keys tab", "keys 1"}},
		{"one multi-select", ask(multi), board.Answer{Picks: [][]int{{1}}}, 0, []string{"keys 2", "keys tab", "keys 1"}},
		{"typed", ask(single), board.Answer{Text: "Medium"}, 0, []string{"keys 3", "send Medium (force)"}},
		{"typed multi-select", ask(single, multi), board.Answer{Picks: [][]int{{0}}, Text: "Pickles"}, 1, []string{"keys esc", "send > Size?\n\nS\n\n> Extras?\n\nPickles"}},
		{"allow", board.Question{App: "claude-code", Kind: "permission"}, board.Answer{Allow: &yes}, 0, []string{"keys 1"}},
		{"deny", board.Question{App: "claude-code", Kind: "permission"}, board.Answer{Allow: &no}, 0, []string{"keys esc"}},
		{"deny with text", board.Question{App: "claude-code", Kind: "permission"}, board.Answer{Allow: &no, Text: "Use make clean"}, 2, []string{"keys esc", "send Use make clean"}},
	} {
		te := &typingExecutor{executorFixture: &executorFixture{}, blocked: c.blocked}
		if err := TypeAnswer(context.Background(), te.Query, "t1", c.q, c.a); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(te.calls, c.want) {
			t.Fatalf("%s: typed %q, want %q", c.name, te.calls, c.want)
		}
	}
}
