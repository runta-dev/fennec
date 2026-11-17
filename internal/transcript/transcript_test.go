package transcript

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestClaudeLines(t *testing.T) {
	cases := []struct {
		line string
		want []Item
	}{
		{`{"type":"user","timestamp":"2026-11-17T10:00:00.000Z","message":{"role":"user","content":"Fix the flaky test"}}`,
			[]Item{{Kind: "user", Text: "Fix the flaky test", Time: 1794909600000}}},
		{`{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name>"}}`, nil},
		{`{"type":"user","isMeta":true,"message":{"role":"user","content":"Caveat: messages below"}}`, nil},
		{`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`, nil},
		{`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"subagent"}]}}`, nil},
		{`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"Found it."},{"type":"tool_use","name":"Bash","input":{"command":"go test ./...","description":"Run the tests"}}]}}`,
			[]Item{{Kind: "agent", Text: "Found it."}, {Kind: "tool", Tool: "ran", Text: "go test ./..."}}},
		{`{"type":"session-owner","sessionId":"x"}`, nil},
		{`not json`, nil},
	}
	for _, c := range cases {
		got := Parse([]byte(c.line), 0)
		if len(got) != len(c.want) {
			t.Fatalf("%s: got %+v", c.line, got)
		}
		for i := range got {
			got[i].ID = 0
			if !reflect.DeepEqual(got[i], c.want[i]) {
				t.Fatalf("%s: item %d = %+v, want %+v", c.line, i, got[i], c.want[i])
			}
		}
	}
}

func TestCodexLines(t *testing.T) {
	user := Parse([]byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"},{"type":"input_text","text":"Add tests"}]}}`), 5)
	if len(user) != 1 || user[0].Kind != "user" || user[0].Text != "Add tests" {
		t.Fatalf("user %+v", user)
	}
	agent := Parse([]byte(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}}`), 6)
	if len(agent) != 1 || agent[0].Kind != "agent" {
		t.Fatalf("agent %+v", agent)
	}
	tool := Parse([]byte(`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"go test\"]}"}}`), 7)
	if len(tool) != 1 || tool[0].Tool != "ran" || tool[0].Text != "go test" {
		t.Fatalf("tool %+v", tool)
	}
	if Parse([]byte(`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"rules"}]}}`), 8) != nil {
		t.Fatal("developer text must stay hidden")
	}
	if user[0].ID == agent[0].ID {
		t.Fatal("ids must differ by line")
	}
}

func TestUserImagesAndInjectedText(t *testing.T) {
	claude := Parse([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Look at [Image #1] this\n[Image: source: /tmp/a.png]"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBO"}}]}}`), 1)
	if len(claude) != 1 || claude[0].Text != "Look at this" || len(claude[0].Images) != 1 || claude[0].Images[0] != "data:image/png;base64,iVBO" {
		t.Fatalf("claude %+v", claude)
	}
	codex := Parse([]byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"What is this?"},{"type":"input_image","image_url":"data:image/jpeg;base64,/9j/"}]}}`), 2)
	if len(codex) != 1 || codex[0].Text != "What is this?" || len(codex[0].Images) != 1 {
		t.Fatalf("codex %+v", codex)
	}
	for _, injected := range []string{"# AGENTS.md instructions for /root/.fen/x\n\n<INSTRUCTIONS>", "\n# Files mentioned by the user:\n\n## a.png", "Codex could not read the local image at x"} {
		line, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": injected}}}})
		if got := Parse(line, 3); got != nil {
			t.Fatalf("%q shown: %+v", injected, got)
		}
	}
}

func TestDescribeReadsLikeCodex(t *testing.T) {
	exec := describe("exec", json.RawMessage(`"const r = await Promise.allSettled([tools.exec_command({cmd:\"uname -a\"}), tools.exec_command({cmd:\"df -h \\\"/\\\"\"})])"`))
	if len(exec) != 2 || exec[0].Text != "uname -a" || exec[1].Text != `df -h "/"` {
		t.Fatalf("exec %+v", exec)
	}
	for _, c := range []struct{ name, input, verb, text string }{
		{"Read", `{"file_path":"/a/b/panel.css"}`, "read", "panel.css"},
		{"Edit", `{"file_path":"/a/health.go"}`, "edited", "health.go"},
		{"Grep", `{"pattern":"TODO"}`, "searched", "TODO"},
		{"Bash", `{"command":"go test ./...\ngo vet ./...","description":"Run"}`, "ran", "go test ./... …"},
		{"write_stdin", `{"session_id":1,"chars":""}`, "waited", "for a running command"},
		{"mcp__magpie__Artifact", `{}`, "called", "Artifact"},
	} {
		got := describe(c.name, json.RawMessage(c.input))
		if len(got) != 1 || got[0].Tool != c.verb || got[0].Text != c.text {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if describe("TodoWrite", nil) != nil {
		t.Error("todo lists are bookkeeping")
	}
}

func TestCompactionIsAMarker(t *testing.T) {
	for _, line := range []string{
		`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued from a previous conversation that ran out of context. Summary: ..."}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"This session is being continued from a previous conversation that ran out of context."}]}}`,
		`{"type":"compacted","payload":{"message":"summary"}}`,
	} {
		got := Parse([]byte(line), 1)
		if len(got) != 1 || got[0].Kind != "compacted" || got[0].Text != "" {
			t.Errorf("%s: %+v", line, got)
		}
	}
}

func TestPiLines(t *testing.T) {
	lines := []string{
		`{"type":"session","version":3,"id":"x","cwd":"/root/.fen/fly-ink"}`,
		`{"type":"message","timestamp":"2026-11-17T15:32:17.085Z","message":{"role":"system","content":"You are pi"}}`,
		`{"type":"message","timestamp":"2026-11-17T15:32:17.086Z","message":{"role":"user","content":[{"type":"text","text":"tell me your sop"},{"type":"image","data":"iVBO","mimeType":"image/png"}]}}`,
		`{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"Checking."},{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"pwd; uname -a","timeout":10}},{"type":"toolCall","name":"edit","arguments":{"path":"/a/b/main.go"}}]}}`,
		`{"type":"message","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"/root"}]}}`,
		`{"type":"compaction","summary":"..."}`,
	}
	var got []Item
	for i, line := range lines {
		for _, item := range Parse([]byte(line), int64(i)) {
			item.ID, item.Time = 0, 0
			got = append(got, item)
		}
	}
	want := []Item{
		{Kind: "user", Text: "tell me your sop", Images: []string{"data:image/png;base64,iVBO"}},
		{Kind: "agent", Text: "Checking."},
		{Kind: "tool", Tool: "ran", Text: "pwd; uname -a"},
		{Kind: "tool", Tool: "edited", Text: "main.go"},
		{Kind: "compacted"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestPastedAndSteeringMessages(t *testing.T) {
	pasted := `{"type":"user","message":{"role":"user","content":"<pasted_content id=\"9c71\">\nUse the same line weight\n</pasted_content id=\"9c71\">\n\n[Image #3]\n\n(The images above, those in tool results too, are attached to this message as image content you can see. Any notes after it are where it keeps them: the image itself is in the message.)\n[Image: source: /tmp/a.png]"}}`
	got := Parse([]byte(pasted), 1)
	if len(got) != 1 || got[0].Kind != "user" || got[0].Text != "Use the same line weight" {
		t.Fatalf("pasted %+v", got)
	}
	steer := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"Command was moved to the background.\n\nThe user sent a new message while you were working:\n[Image #67] 引入新bug了\n\nThis is how Claude Code surfaces messages the user sends mid-turn."},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBO"}}]}]}}`
	got = Parse([]byte(steer), 2)
	if len(got) != 1 || got[0].Kind != "user" || got[0].Text != "引入新bug了" || len(got[0].Images) != 1 {
		t.Fatalf("steer %+v", got)
	}
	plain := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}}`
	if got := Parse([]byte(plain), 3); len(got) != 0 {
		t.Fatalf("plain tool result %+v", got)
	}
}

func TestQueuedMessages(t *testing.T) {
	queued := `{"type":"attachment","attachment":{"type":"queued_command","prompt":"漏用户消息和agent回复","commandMode":"prompt","origin":{"kind":"human"},"humanTurn":true}}`
	if got := Parse([]byte(queued), 1); len(got) != 1 || got[0].Kind != "user" || got[0].Text != "漏用户消息和agent回复" {
		t.Fatalf("queued %+v", got)
	}
	beside := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"done"},{"type":"text","text":"The user sent a new message while you were working:\nfix it\n\nThis is how Claude Code surfaces messages the user sends mid-turn."}]}}`
	if got := Parse([]byte(beside), 2); len(got) != 1 || got[0].Text != "fix it" {
		t.Fatalf("beside %+v", got)
	}
	if Parse([]byte(`{"type":"attachment","attachment":{"type":"queued_command","prompt":"x","origin":{"kind":"task-notification"}}}`), 3) != nil {
		t.Fatal("only people's messages")
	}
}

func TestKimiLines(t *testing.T) {
	lines := []string{
		`{"type":"profile.bind","agentId":"main","systemPrompt":"You are Kimi"}`,
		`{"type":"context.append_message","agentId":"main","message":{"role":"user","content":[{"type":"text","text":"who are you"}],"origin":{"kind":"user"}},"time":1794930938009}`,
		`{"type":"context.append_message","agentId":"main","message":{"role":"user","content":[{"type":"text","text":"<system-reminder>date</system-reminder>"}],"origin":{"kind":"injection"}},"time":1794930938010}`,
		`{"type":"context.append_loop_event","agentId":"main","event":{"type":"content.part","part":{"type":"think","think":"hm"}},"time":1}`,
		`{"type":"context.append_loop_event","agentId":"main","event":{"type":"tool.call","name":"Bash","args":{"command":"uname -sm && pwd"}},"time":2}`,
		`{"type":"context.append_loop_event","agentId":"main","event":{"type":"tool.result","result":{"output":"Linux"}},"time":3}`,
		`{"type":"context.append_loop_event","agentId":"main","event":{"type":"content.part","part":{"type":"text","text":"I'm **Kimi**."}},"time":4}`,
		`{"type":"context.append_loop_event","agentId":"coder","event":{"type":"content.part","part":{"type":"text","text":"subagent"}},"time":5}`,
	}
	var got []Item
	for i, line := range lines {
		for _, item := range Parse([]byte(line), int64(i)) {
			item.ID = 0
			got = append(got, item)
		}
	}
	want := []Item{
		{Kind: "user", Text: "who are you", Time: 1794930938009},
		{Kind: "tool", Tool: "ran", Text: "uname -sm && pwd", Time: 2},
		{Kind: "agent", Text: "I'm **Kimi**.", Time: 4},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestQuotedSteeringIsOutput(t *testing.T) {
	quoted := "grep found:\nThe user sent a new message while you were working:\nold message\n\nThis is how Claude Code surfaces messages the user sends mid-turn.\nmore output\n"
	if got := steeredText(quoted); len(got) != 0 {
		t.Fatalf("quoted output read as a message: %+v", got)
	}
	appended := quoted + "\nThe user sent a new message while you were working:\nfirst\n\nThis is how Claude Code surfaces messages x.\n\nThe user sent a new message while you were working:\nsecond\n\nThis is how Claude Code surfaces messages x.\n[Image: source: /tmp/a.png]\n"
	got := steeredText(appended)
	if len(got) != 2 || got[0].Text != "first" || got[1].Text != "second" {
		t.Fatalf("appended %+v", got)
	}
}

func TestMessagesInTaskNotification(t *testing.T) {
	prompt := "<task-notification>\n<result>\nbuild ok\n\nThe user sent a new message while you were working:\nfirst\n\nThis is how Claude Code surfaces messages x.\n\n\nThe user sent a new message while you were working:\n[Image #73] second\n\nThis is how Claude Code surfaces messages x.\n[image result] Binary content (image/png, 250KB) saved to /tmp/r.png\n[Image: source: /tmp/b.png]\n</result>\n</task-notification>"
	line, _ := json.Marshal(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "queued_command", "prompt": prompt, "commandMode": "task-notification", "origin": map[string]any{"kind": "task-notification"}}})
	got := Parse(line, 1)
	if len(got) != 2 || got[0].Text != "first" || got[1].Text != "second" {
		t.Fatalf("got %+v", got)
	}
}
