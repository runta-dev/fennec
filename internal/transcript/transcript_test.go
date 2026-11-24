package transcript

import (
	"encoding/json"
	"reflect"
	"testing"
)

// parseAll feeds lines in order and drops the ids and times, which only
// identify lines.
func parseAll(lines ...string) []Item {
	items := []Item{}
	for i, line := range lines {
		for _, item := range Parse([]byte(line), int64(i)) {
			item.ID, item.Time = 0, 0
			items = append(items, item)
		}
	}
	return items
}

func expect(t *testing.T, got []Item, want ...Item) {
	t.Helper()
	if len(want) == 0 {
		want = []Item{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestClaudeTurn(t *testing.T) {
	got := parseAll(
		`{"type":"user","timestamp":"2026-11-17T10:00:00.000Z","message":{"role":"user","content":"Fix the flaky test"}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hm"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Running the tests."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./...","description":"Run"}}]}}`,
		// Tool results answer the call: not a message, not a turn boundary.
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Fixed."}]}}`,
		`{"type":"system","subtype":"turn_duration","durationMs":3592}`,
	)
	expect(t, got,
		Item{Kind: "user", Text: "Fix the flaky test"},
		Item{Kind: "agent", Text: "Running the tests."},
		Item{Kind: "tool", Tool: "ran", Text: "go test ./..."},
		Item{Kind: "agent", Text: "Fixed."},
		Item{Kind: "end", Duration: 3592},
	)
}

func TestClaudeSkipsWhatThePersonDidNotType(t *testing.T) {
	expect(t, parseAll(
		`{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name>"}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"Caveat: messages below"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"<system-reminder>\nToday is…\n</system-reminder>"}]}}`,
		`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"subagent"}]}}`,
		`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued…"}}`,
		`{"type":"attachment","attachment":{"type":"queued_command","prompt":"<task-notification>done</task-notification>","origin":{"kind":"task-notification"}}}`,
		`{"type":"system","subtype":"stop_hook_summary"}`,
		`{"type":"last-prompt","lastPrompt":"x"}`,
		`{"type":"user","message":{"role":"user","content":"[SUGGESTION MODE: Suggest what the user might type next]"}}`,
		`not json`,
	))
}

func TestClaudeMarkers(t *testing.T) {
	expect(t, parseAll(
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`,
		`{"type":"system","subtype":"compact_boundary","content":"Conversation compacted"}`,
		`{"type":"assistant","isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: 529"}]}}`,
	),
		Item{Kind: "end", Reason: "interrupted"},
		Item{Kind: "compacted"},
		Item{Kind: "end", Reason: "error"},
	)
}

func TestClaudeMessagesSentWhileWorking(t *testing.T) {
	expect(t, parseAll(
		`{"type":"attachment","attachment":{"type":"queued_command","prompt":"also check the table","origin":{"kind":"human"},"humanTurn":true}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"done"},{"type":"text","text":"The user sent a new message while you were working:\nfix it\n\nThis is how Claude Code surfaces messages the user sends mid-turn."}]}}`,
	),
		Item{Kind: "user", Text: "also check the table", Steer: true},
		Item{Kind: "user", Text: "fix it", Steer: true},
	)
	// The same words inside a tool's output are output.
	expect(t, parseAll(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"The user sent a new message while you were working:\nold\n\nThis is how Claude Code surfaces messages x."}]}}`))
}

func TestUserImagesAndPastedText(t *testing.T) {
	expect(t, parseAll(
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Look at [Image #1] this\n[Image: source: /tmp/a.png]"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBO"}}]}}`,
		`{"type":"user","message":{"role":"user","content":"<pasted_content id=\"9c\">\nUse the same line weight\n</pasted_content id=\"9c\">"}}`,
		`{"type":"user","message":{"role":"user","content":"<div>pasted HTML</div>"}}`,
	),
		Item{Kind: "user", Text: "Look at this", Images: []string{"data:image/png;base64,iVBO"}},
		Item{Kind: "user", Text: "Use the same line weight"},
		Item{Kind: "user", Text: "<div>pasted HTML</div>"},
	)
}

func TestCodexTurn(t *testing.T) {
	expect(t, parseAll(
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"rules"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"},{"type":"input_text","text":"<image name=[Image #1] path=\"/tmp/a.png\">"},{"type":"input_image","image_url":"data:image/jpeg;base64,/9j/"},{"type":"input_text","text":"</image>"},{"type":"input_text","text":"[Image #1] Add tests"}]}}`,
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"go test\"]}"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","output":"ok"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}}`,
		`{"type":"event_msg","payload":{"type":"task_complete","duration_ms":241897,"last_agent_message":"Done."}}`,
		`{"type":"event_msg","payload":{"type":"turn_aborted","reason":"interrupted","duration_ms":3000}}`,
		`{"type":"compacted","payload":{"message":"summary"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /root/.fen/x\n\n<INSTRUCTIONS>"}]}}`,
	),
		Item{Kind: "user", Text: "Add tests", Images: []string{"data:image/jpeg;base64,/9j/"}},
		Item{Kind: "tool", Tool: "ran", Text: "go test"},
		Item{Kind: "agent", Text: "Done."},
		Item{Kind: "end", Duration: 241897},
		Item{Kind: "end", Duration: 3000, Reason: "interrupted"},
		Item{Kind: "compacted"},
	)
}

func TestPiTurn(t *testing.T) {
	expect(t, parseAll(
		`{"type":"session","version":3,"id":"x","cwd":"/root/.fen/fly-ink"}`,
		`{"type":"message","message":{"role":"system","content":"You are pi"}}`,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"tell me your sop"},{"type":"image","data":"iVBO","mimeType":"image/png"}]}}`,
		`{"type":"message","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"Checking."},{"type":"toolCall","name":"bash","arguments":{"command":"pwd; uname -a"}},{"type":"toolCall","name":"edit","arguments":{"path":"/a/b/main.go"}}]}}`,
		`{"type":"message","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"/root"}]}}`,
		`{"type":"message","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"Here it is."}]}}`,
		`{"type":"message","message":{"role":"assistant","stopReason":"aborted","content":[]}}`,
		`{"type":"message","message":{"role":"assistant","stopReason":"error","errorMessage":"402 Insufficient balance","content":[]}}`,
		`{"type":"compaction","summary":"..."}`,
	),
		Item{Kind: "user", Text: "tell me your sop", Images: []string{"data:image/png;base64,iVBO"}},
		Item{Kind: "agent", Text: "Checking."},
		Item{Kind: "tool", Tool: "ran", Text: "pwd; uname -a"},
		Item{Kind: "tool", Tool: "edited", Text: "main.go"},
		Item{Kind: "agent", Text: "Here it is."},
		Item{Kind: "end"},
		Item{Kind: "end", Reason: "interrupted"},
		Item{Kind: "agent", Text: "402 Insufficient balance"},
		Item{Kind: "end", Reason: "error"},
		Item{Kind: "compacted"},
	)
}

func TestKimiTurn(t *testing.T) {
	expect(t, parseAll(
		`{"type":"profile.bind","agentId":"main","systemPrompt":"You are Kimi"}`,
		`{"type":"context.append_message","agentId":"main","message":{"role":"user","content":[{"type":"text","text":"who are you"}],"origin":{"kind":"user"}},"time":1794930938009}`,
		`{"type":"context.append_message","agentId":"main","message":{"role":"user","content":[{"type":"text","text":"<system-reminder>date</system-reminder>"}],"origin":{"kind":"injection"}},"time":2}`,
		`{"type":"context.append_loop_event","agentId":"main","event":{"type":"content.part","part":{"type":"think","think":"hm"}},"time":3}`,
		`{"type":"context.append_loop_event","agentId":"main","event":{"type":"tool.call","name":"Bash","args":{"command":"uname -sm && pwd"}},"time":4}`,
		`{"type":"context.append_loop_event","agentId":"main","event":{"type":"tool.result","result":{"output":"Linux"}},"time":5}`,
		`{"type":"context.append_loop_event","agentId":"main","event":{"type":"content.part","part":{"type":"text","text":"I'm **Kimi**."}},"time":6}`,
		`{"type":"context.append_loop_event","agentId":"coder","event":{"type":"content.part","part":{"type":"text","text":"subagent"}},"time":7}`,
		`{"type":"turn.ended","agentId":"main","turnId":0,"reason":"completed","durationMs":15450,"time":8}`,
	),
		Item{Kind: "user", Text: "who are you"},
		Item{Kind: "tool", Tool: "ran", Text: "uname -sm && pwd"},
		Item{Kind: "agent", Text: "I'm **Kimi**."},
		Item{Kind: "end", Duration: 15450},
	)
}

func TestIdsDifferByLine(t *testing.T) {
	a := Parse([]byte(`{"type":"user","message":{"role":"user","content":"a"}}`), 5)
	b := Parse([]byte(`{"type":"user","message":{"role":"user","content":"b"}}`), 6)
	if a[0].ID == b[0].ID {
		t.Fatal("ids must differ by line")
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
