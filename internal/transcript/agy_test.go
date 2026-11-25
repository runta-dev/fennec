package transcript

import "testing"

// Lines as agy 1.5 writes transcript_full.jsonl.
func TestAgyTranscript(t *testing.T) {
	lines := []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-25T09:00:00Z","content":"<USER_REQUEST>\nread a.txt\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-11-25T17:00:00+08:00.\n</ADDITIONAL_METADATA>"}`,
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-25T09:00:01Z","thinking":"I should read it.","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/work/a.txt"}},{"name":"run_command","args":{"CommandLine":"go test ./...\necho done","Cwd":"/work"}},{"name":"task_boundary","args":{"TaskName":"x"}}]}`,
		`{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-25T09:00:02Z","content":"Created At: …\n1: hi"}`,
		`{"step_index":3,"source":"SYSTEM","type":"SYSTEM_MESSAGE","status":"DONE","created_at":"2026-11-25T09:00:02Z","content":"<SYSTEM_MESSAGE>notice</SYSTEM_MESSAGE>"}`,
		`{"step_index":4,"source":"SYSTEM","type":"ERROR_MESSAGE","status":"DONE","error":"API error (attempt 1)","created_at":"2026-11-25T09:00:03Z"}`,
		`{"step_index":5,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-25T09:00:04Z","content":"The file says hi."}`,
		`{"step_index":6,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-25T09:01:00Z"}`,
	}
	var items []Item
	for i, line := range lines {
		items = append(items, Parse([]byte(line), int64(i*1000))...)
	}
	want := []Item{
		{Kind: "user", Text: "read a.txt"},
		{Kind: "tool", Tool: "read", Text: "a.txt"},
		{Kind: "tool", Tool: "ran", Text: "go test ./... …"},
		{Kind: "agent", Text: "The file says hi."},
		{Kind: "end"},
	}
	if len(items) != len(want) {
		t.Fatalf("%d items: %+v", len(items), items)
	}
	for i, item := range items {
		if item.Kind != want[i].Kind || item.Text != want[i].Text || item.Tool != want[i].Tool {
			t.Fatalf("item %d = %+v, want %+v", i, item, want[i])
		}
	}
	if items[0].Time != 1795597200000 {
		t.Fatal("created_at not read", items[0].Time)
	}
}
