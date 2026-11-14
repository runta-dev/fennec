package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	conversationA = "af4ac027-381a-48d8-9cbc-32ba6f82c5fb"
	conversationB = "0b1f8e2c-1111-4222-8333-944455556666"
	launchID      = "brisk-falcon"
)

func writeFixture(t *testing.T, file, content string, modified time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !modified.IsZero() {
		os.Chtimes(file, modified, modified)
	}
}

func TestParseResume(t *testing.T) {
	for _, test := range []struct {
		agent string
		args  []string
		id    string
		ok    bool
	}{
		{"claude", []string{"--resume", conversationA}, conversationA, true},
		{"claude", []string{"-r", conversationA, "fix it"}, conversationA, true},
		{"claude", []string{"--resume=" + conversationA}, conversationA, true},
		{"claude", []string{"--resume"}, "", false},             // picker
		{"claude", []string{"--resume", "auth bug"}, "", false}, // picker search
		{"claude", []string{"--model", "--resume", "x"}, "", false},
		{"claude", []string{"--", "--resume", conversationA}, "", false},
		{"claude", []string{"-p", "hello"}, "", false},
		{"codex", []string{"resume", conversationA}, conversationA, true},
		{"codex", []string{"-m", "gpt-5", "resume", conversationA}, conversationA, true},
		{"codex", []string{"resume"}, "", false},
		{"codex", []string{"exec", "resume", conversationA}, "", false},
		{"codex", []string{"say", conversationA}, "", false},
		{"pi", []string{"--session", conversationA}, "", false},
	} {
		request, ok := parseResume(test.agent, test.args)
		if ok != test.ok || request.id != test.id {
			t.Errorf("%s %q: got %q %v", test.agent, test.args, request.id, ok)
		}
	}
	request, ok := parseResume("claude", []string{"--model", "opus", "-c", "go on"})
	if !ok || !request.latest || !reflect.DeepEqual(request.rewrite(conversationA), []string{"--model", "opus", "--resume", conversationA, "go on"}) {
		t.Fatalf("continue: %+v %v", request, ok)
	}
	request, ok = parseResume("codex", []string{"resume", "--last", "--all"})
	if !ok || !request.latest || !request.all || !reflect.DeepEqual(request.rewrite(conversationA), []string{"resume", conversationA}) {
		t.Fatalf("codex last: %+v %v", request, ok)
	}
}

func TestClaudeConversationMovesToTheCloudWorkspace(t *testing.T) {
	mac, cloud := t.TempDir(), t.TempDir()
	project := filepath.Join(mac, "projects", ClaudeProjectDirectory("/Users/me/repo"))
	old := time.Now().Add(-time.Hour)
	writeFixture(t, filepath.Join(project, conversationB+".jsonl"), `{"cwd":"/Users/me/repo"}`+"\n", old)
	writeFixture(t, filepath.Join(project, conversationA+".jsonl"), `{"type":"user","uuid":"u1","cwd":"/Users/me/repo","message":"hi"}`+"\n"+`{"type":"assistant","uuid":"u2","cwd":"/Users/me/repo/sub"}`+"\n", time.Now())
	writeFixture(t, filepath.Join(project, conversationA, "subagents", "agent-1.jsonl"), `{"cwd":"/Users/me/repo"}`+"\n", time.Time{})
	writeFixture(t, filepath.Join(mac, "projects", "-elsewhere", "unrelated.jsonl"), "{}\n", time.Time{})

	// --continue resolves to the newest conversation of this directory.
	bundle, args, err := CollectLocalSession(mac, "claude", "/Users/me/repo", launchID, []string{"-c"})
	if err != nil || bundle == nil || bundle.Conversation != conversationA || !reflect.DeepEqual(args, []string{"--resume", conversationA}) {
		t.Fatalf("collect: %+v %v %v", bundle, args, err)
	}
	if len(bundle.Files) != 2 {
		t.Fatalf("files: %+v", bundle.Files)
	}
	payload, err := EncodeSessionImport(bundle)
	if err != nil {
		t.Fatal(err)
	}
	written, err := ReceiveSessionImport(cloud, bytes.NewReader(payload))
	if err != nil || written != 2 {
		t.Fatalf("apply: %d %v", written, err)
	}
	// No agent-mode file means native mode.
	workspace := filepath.Join(cloud, ".fen", launchID)
	target := filepath.Join(cloud, ".local/fennec/native/claude/projects", ClaudeProjectDirectory(workspace))
	data, err := os.ReadFile(filepath.Join(target, conversationA+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/Users/me") || strings.Count(string(data), `"cwd":"`+workspace+`"`) != 2 || !strings.Contains(string(data), `"message":"hi"`) {
		t.Fatalf("transcript: %s", data)
	}
	if _, err := os.Stat(filepath.Join(target, conversationA, "subagents", "agent-1.jsonl")); err != nil {
		t.Fatal("subagent transcript not moved")
	}

	// The cloud now has the latest record: the Mac checks first and sends
	// nothing, and an import that arrives anyway changes nothing.
	if !HasCloudSession(cloud, "claude", conversationA, bundle.Marker) {
		t.Fatal("imported conversation not found by marker")
	}
	if HasCloudSession(cloud, "claude", conversationA, "u9") || HasCloudSession(cloud, "codex", conversationA, bundle.Marker) {
		t.Fatal("presence check matched the wrong record or agent")
	}
	longer := string(data) + `{"type":"user","uuid":"u3","cwd":"x"}` + "\n"
	os.WriteFile(filepath.Join(target, conversationA+".jsonl"), []byte(longer), 0o600)
	if written, _ = ApplySessionImport(cloud, *bundle); written != 0 {
		t.Fatalf("cloud copy replaced: %d", written)
	}

	// Resumed again from the Mac in a new cloud session: the cloud copy that was
	// continued (in the earlier session's folder) stays the one Claude loads.
	next := *bundle
	next.LaunchID = "quiet-otter"
	if written, _ = ApplySessionImport(cloud, next); written != 0 {
		t.Fatalf("older Mac copy shadowed the continued cloud copy: %d", written)
	}

	// A conversation the Mac does not have is left for the cloud to resolve.
	bundle, args, err = CollectLocalSession(mac, "claude", "/Users/me/repo", launchID, []string{"--resume", "11111111-2222-4333-8444-555555555555"})
	if bundle != nil || err != nil || len(args) != 2 {
		t.Fatalf("unknown conversation: %+v %v", bundle, err)
	}
}

func TestCodexConversationMovesWithItsProvider(t *testing.T) {
	mac, cloud := t.TempDir(), t.TempDir()
	rollout := `{"type":"session_meta","payload":{"id":"` + conversationA + `","cwd":"/Users/me/repo","model_provider":"openai"}}` + "\n" + `{"timestamp":"2026-11-14T10:00:05.123Z","type":"turn_context","payload":{"cwd":"/Users/me/repo"}}` + "\n"
	name := "sessions/2026/11/14/rollout-2026-11-14T10-00-00-" + conversationA + ".jsonl"
	writeFixture(t, filepath.Join(mac, name), rollout, time.Now())
	writeFixture(t, filepath.Join(mac, "sessions/2026/11/13/rollout-2026-11-13T10-00-00-"+conversationB+".jsonl"), `{"type":"session_meta","payload":{"id":"`+conversationB+`","cwd":"/Users/me/other"}}`+"\n", time.Now().Add(time.Hour))

	bundle, args, err := CollectLocalSession(mac, "codex", "/Users/me/repo", launchID, []string{"resume", "--last"})
	if err != nil || bundle == nil || bundle.Conversation != conversationA || !reflect.DeepEqual(args, []string{"resume", conversationA}) || bundle.Files[0].Path != name {
		t.Fatalf("collect: %+v %v %v", bundle, args, err)
	}
	// Gateway mode: the cloud Codex runs from ~/.codex under the magpie provider.
	writeFixture(t, filepath.Join(cloud, ".local/fennec/agent-mode.json"), `{"native":false}`, time.Time{})
	writeFixture(t, filepath.Join(cloud, ".codex/config.toml"), "model_provider = \"magpie\"\n[model_providers.magpie]\nname = \"x\"\n", time.Time{})
	if written, err := ApplySessionImport(cloud, *bundle); err != nil || written != 1 {
		t.Fatalf("apply: %d %v", written, err)
	}
	data, _ := os.ReadFile(filepath.Join(cloud, ".codex", name))
	workspace := filepath.Join(cloud, ".fen", launchID)
	if !strings.Contains(string(data), `"model_provider":"magpie"`) || strings.Contains(string(data), "/Users/me") || strings.Count(string(data), workspace) != 2 {
		t.Fatalf("rollout: %s", data)
	}
	if bundle.Marker != "2026-11-14T10:00:05.123Z" || !HasCloudSession(cloud, "codex", conversationA, bundle.Marker) {
		t.Fatalf("codex marker %q not found in the cloud copy", bundle.Marker)
	}
}

func TestTrimClaudeTranscriptKeepsWhatResumeLoads(t *testing.T) {
	lines := []string{
		`{"type":"mode","mode":"default"}`,
		`{"type":"user","uuid":"old1","parentUuid":null,"message":"old screenshot AAAA"}`,
		`{"type":"api-request-blob","data":"huge"}`,
		`{"type":"assistant","uuid":"keep1","parentUuid":"old1"}`,
		`{"type":"user","uuid":"keep2","parentUuid":"keep1"}`,
		`{"type":"assistant","uuid":"seg1","parentUuid":"keep2"}`,
		`{"type":"user","uuid":"seg2","parentUuid":"seg1"}`,
		`{"type":"ai-title","title":"t"}`,
		`not json`,
		`{"type":"system","subtype":"compact_boundary","parentUuid":null,"uuid":"b","compactMetadata":{"preservedMessages":{"allUuids":["keep1","keep2"]},"preservedSegment":{"headUuid":"seg1","tailUuid":"seg2"}}}`,
		`{"type":"user","uuid":"s","parentUuid":"b","isCompactSummary":true}`,
		`{"type":"api-request","after":"kept"}`,
		`{"type":"assistant","uuid":"last","parentUuid":"s"}`,
	}
	got := string(trimClaudeTranscript([]byte(strings.Join(lines, "\n") + "\n")))
	want := strings.Join([]string{lines[0], lines[3], lines[4], lines[5], lines[6], lines[7], lines[9], lines[10], lines[11], lines[12]}, "\n") + "\n"
	if got != want {
		t.Fatalf("trimmed:\n%s\nwant:\n%s", got, want)
	}
	if sessionMarker("claude", []byte(got)) != "last" {
		t.Fatal("marker is not the last record")
	}
	// Without a compaction the transcript is sent whole.
	whole := strings.Join(lines[:9], "\n") + "\n"
	if string(trimClaudeTranscript([]byte(whole))) != whole {
		t.Fatal("uncompacted transcript changed")
	}
}

func TestSessionImportRejectsUnsafePaths(t *testing.T) {
	for _, file := range []string{"../x.jsonl", "/etc/passwd", conversationA + "/../../x", "other.jsonl"} {
		bundle := SessionImport{Version: 1, Agent: "claude", LaunchID: launchID, Conversation: conversationA, Marker: "u1", Files: []SessionFile{{Path: file, Data: []byte("{}\n")}}}
		if _, err := ApplySessionImport(t.TempDir(), bundle); err == nil {
			t.Errorf("accepted %q", file)
		}
	}
	bundle := SessionImport{Version: 1, Agent: "codex", LaunchID: launchID, Conversation: conversationA, Marker: "u1", Files: []SessionFile{{Path: "config.toml", Data: []byte("x")}}}
	if _, err := ApplySessionImport(t.TempDir(), bundle); err == nil {
		t.Error("codex accepted a non-rollout path")
	}
	bundle = SessionImport{Version: 1, Agent: "claude", LaunchID: "../x", Conversation: conversationA, Marker: "u1", Files: []SessionFile{{Path: conversationA + ".jsonl"}}}
	if _, err := ApplySessionImport(t.TempDir(), bundle); err == nil {
		t.Error("accepted an invalid launch ID")
	}
	bundle = SessionImport{Version: 1, Agent: "claude", LaunchID: launchID, Conversation: conversationA, Marker: "x; rm -rf /", Files: []SessionFile{{Path: conversationA + ".jsonl"}}}
	if _, err := ApplySessionImport(t.TempDir(), bundle); err == nil {
		t.Error("accepted a marker outside the command-line character set")
	}
}
