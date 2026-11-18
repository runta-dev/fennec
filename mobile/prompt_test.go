package fennecmobile

import (
	"strings"
	"testing"
)

// Screens modeled on the agents' current prompts (framing, markers, key
// hints and footers), with the conversation above them.
const claudeApproval = `⏺ I'll list the files first.

╭──────────────────────────────────────────────────────────────╮
│ Bash command                                                 │
│                                                              │
│   ls -la /tmp/relay-mac-demo                                 │
│   List the fixture's files                                   │
│                                                              │
│ Do you want to proceed?                                      │
│ ❯ 1. Yes                                                     │
│   2. Yes, and don't ask again for ls commands in             │
│      /tmp/relay-mac-demo                                     │
│   3. No, and tell Claude what to do differently (esc)        │
╰──────────────────────────────────────────────────────────────╯
`

const codexApproval = `• I'll run the tests on your Mac.

  Would you like to run the following command?

  $ go test -race ./...

› 1. Yes, proceed (y)
  2. Yes, and don't ask again for this command (p)
  3. No, and tell Codex what to do differently (esc)

  Press enter to confirm or esc to cancel
`

const question = `Which update channel should I use?

  1. APNs push from Runta
  2. In-app updates only
  3. Broadcast channel
`

func TestParsePromptReadsClaudeCodeApproval(t *testing.T) {
	p, ok := ParsePrompt(claudeApproval)
	if !ok {
		t.Fatal("no prompt")
	}
	if p.Question != "Do you want to proceed?" {
		t.Fatalf("question %q", p.Question)
	}
	if !strings.Contains(p.Detail, "ls -la /tmp/relay-mac-demo") || strings.Contains(p.Detail, "I'll list") {
		t.Fatalf("detail %q", p.Detail)
	}
	want := []string{"Yes", "Yes, and don't ask again for ls commands in /tmp/relay-mac-demo", "No, and tell Claude what to do differently"}
	if len(p.Options) != 3 {
		t.Fatalf("options %+v", p.Options)
	}
	for i, title := range want {
		if p.Options[i].Key != string(rune('1'+i)) || p.Options[i].Title != title {
			t.Fatalf("option %d: %+v", i, p.Options[i])
		}
	}
}

func TestParsePromptReadsCodexApprovalAndQuestion(t *testing.T) {
	p, ok := ParsePrompt(codexApproval)
	if !ok || p.Question != "Would you like to run the following command?" || p.Detail != "$ go test -race ./..." {
		t.Fatalf("%v %+v", ok, p)
	}
	if len(p.Options) != 3 || p.Options[0].Title != "Yes, proceed" || p.Options[2].Title != "No, and tell Codex what to do differently" {
		t.Fatalf("options %+v", p.Options)
	}
	q, ok := ParsePrompt(question)
	if !ok || q.Question != "Which update channel should I use?" || q.Detail != "" || len(q.Options) != 3 {
		t.Fatalf("%v %+v", ok, q)
	}
}

func TestParsePromptIgnoresScreensWithoutAMenu(t *testing.T) {
	for _, screen := range []string{
		"",
		"⏺ Done. All tests pass.\n\n> ",
		// A numbered list in the agent's reply, not a menu at the bottom.
		"Steps:\n1. Read\n2. Fix\n3. Test\n\nI finished all three.\n" + strings.Repeat("more output\n", 30) + "> ",
		// Numbers that don't start at 1 or skip.
		"Pick:\n2. b\n3. c\n",
		"Pick:\n1. a\n3. c\n",
		// One option is not a choice.
		"Continue?\n1. Yes\n",
	} {
		if p, ok := ParsePrompt(screen); ok {
			t.Errorf("menu found in %q: %+v", screen, p)
		}
	}
}

func TestAnswerAcceptsOnlyOptionKeys(t *testing.T) {
	s := &Sessions{}
	for _, key := range []string{"", "0", "10", "enter", "1\n", "y", "1; rm -rf ~", "ctrl+c"} {
		if _, err := s.Answer("t1", strings.Repeat("a", 32), key); err == nil || !strings.Contains(err.Error(), "invalid answer") {
			t.Errorf("key %q accepted: %v", key, err)
		}
	}
	if _, err := s.Answer("t1; ls", strings.Repeat("a", 32), "1"); err == nil {
		t.Error("bad address accepted")
	}
}
