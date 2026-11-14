package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func jsonLines(values ...any) []byte {
	var b bytes.Buffer
	for _, v := range values {
		b.Write(encodeAny(v))
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func TestCodexCompactionPreservesCheckpointAndMetadata(t *testing.T) {
	old := strings.Repeat("obsolete-screenshot", 10000)
	lines := []string{
		`{"type":"session_meta","payload":{"id":"` + conversationA + `","cwd":"/local"}}`,
		`{"type":"turn_context","payload":{"model":"test-model","effort":"high"}}`,
		`{"type":"compacted","payload":{"message":"older summary","replacement_history":[{"old":"` + old + `"}],"window_number":1}}`,
		`{"type":"response_item","payload":{"type":"message","content":[{"type":"input_image","image_url":"` + old + `"}]}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"keep turn boundary","images":["` + old + `"]}}`,
		`{"type":"compacted","payload":{"message":"summary","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"retained"}]}],"window_number":2,"resume_metadata":{"previous_turn_settings":{"model":"test-model"}}}}`,
		`{"type":"response_item","timestamp":"2026-11-14T10:00:00Z","payload":{"type":"function_call_output","call_id":"call1","output":"fresh"}}`,
	}
	original := []byte(strings.Join(lines, "\n") + "\n")
	got := trimCodexTranscript(original)
	if len(got) > len(original)/20 || bytes.Contains(got, []byte(old)) {
		t.Fatal("old image not removed")
	}
	for _, line := range []string{lines[0], lines[1], lines[5], lines[6]} {
		if !bytes.Contains(got, []byte(line)) {
			t.Fatalf("lost checkpoint or metadata: %s", line)
		}
	}
	for _, tail := range []string{`{"type":"event_msg","payload":{"type":"thread_rolled_back","num_turns":1}}`, `{"type":"compacted","payload":{"message":"legacy"}}`, `{"type":"compacted","payload":{"replacement_history":null}}`, `broken`} {
		data := append(append([]byte{}, original...), []byte(tail+"\n")...)
		if !bytes.Equal(trimCodexTranscript(data), data) {
			t.Fatalf("unsafe trim for %s", tail)
		}
	}
	if !bytes.Equal(trimCodexTranscript([]byte(lines[0]+"\n"+lines[3]+"\n")), []byte(lines[0]+"\n"+lines[3]+"\n")) {
		t.Fatal("uncompacted changed")
	}
}

func piCompactionFixture() []byte {
	return []byte(`{"type":"session","version":3,"id":"` + conversationA + `","cwd":"/Users/me/repo"}
{"type":"model_change","id":"m1","parentId":null,"provider":"test","modelId":"before"}
{"type":"message","id":"a1","parentId":"m1","message":{"role":"assistant","provider":"test","model":"after","content":[{"type":"text","text":"` + strings.Repeat("old-image", 20000) + `"}]}}
{"type":"message","id":"u1","parentId":"a1","message":{"role":"user","content":[{"type":"text","text":"kept user"}]}}
{"type":"message","id":"t1","parentId":"u1","message":{"role":"toolResult","toolCallId":"call1","content":[{"type":"text","text":"kept result"}]}}
{"type":"compaction","id":"c1","parentId":"t1","firstKeptEntryId":"u1","summary":"summary","tokensBefore":20000,"timestamp":"2026-11-14T10:00:00Z"}
{"type":"thinking_level_change","id":"s1","parentId":"c1","thinkingLevel":"high"}
{"type":"context_edit","id":"e1","parentId":"s1","targetId":"u1","replacement":{"content":"edited user"}}
{"type":"message","id":"u2","parentId":"e1","message":{"role":"user","content":[{"type":"text","text":"continue"}]}}
`)
}

func TestPiCompactionRetainsTreeSettingsAndContext(t *testing.T) {
	data := piCompactionFixture()
	got := trimPiTranscript(data)
	if len(got) > len(data)/20 || !bytes.Contains(got, []byte(`"model":"after"`)) || !bytes.Contains(got, []byte(`"parentId":"a1"`)) {
		t.Fatal("Pi checkpoint or settings lost")
	}
	for _, v := range []struct{ old, new string }{{`"firstKeptEntryId":"u1"`, `"firstKeptEntryId":"missing"`}, {`"parentId":"e1"`, `"parentId":"a1"`}, {`"version":3`, `"version":1`}, {`"id":"u2"`, `"id":"u1"`}} {
		invalid := bytes.Replace(data, []byte(v.old), []byte(v.new), 1)
		if !bytes.Equal(trimPiTranscript(invalid), invalid) {
			t.Fatal("unsafe Pi trim", v)
		}
	}
}

func TestPiNativeSessionProjection(t *testing.T) {
	module := os.Getenv("FENNEC_PI_SESSION_MODULE")
	if module == "" {
		t.Skip("set FENNEC_PI_SESSION_MODULE to the installed Pi session-manager.js")
	}
	original, trimmed := filepath.Join(t.TempDir(), "before.jsonl"), filepath.Join(t.TempDir(), "after.jsonl")
	data := piCompactionFixture()
	os.WriteFile(original, data, 0600)
	os.WriteFile(trimmed, trimPiTranscript(data), 0600)
	// Exercise the installed agent's own tree walker, settings and context-edit
	// projection. No provider request or real user session is involved.
	script := `import fs from 'node:fs'; import {pathToFileURL} from 'node:url';
 const m=await import(pathToFileURL(process.argv[1]));
 const load=p=>m.buildSessionContext(m.parseSessionEntries(fs.readFileSync(p,'utf8')).filter(e=>e.type!=='session'));
 if(JSON.stringify(load(process.argv[2]))!==JSON.stringify(load(process.argv[3])))process.exit(1);`
	cmd := exec.Command("node", "--input-type=module", "-e", script, module, original, trimmed)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native projection: %v %s", err, output)
	}
}

func kimiCompactionFixture() []byte {
	return []byte(`{"type":"metadata","protocol_version":"1.0","created_at":100}
{"type":"context.append_message","message":{"role":"user","content":[{"type":"text","text":"keep user"}],"toolCalls":[]}}
{"type":"context.append_loop_event","event":{"type":"step.begin","uuid":"step1"}}
{"type":"context.append_loop_event","event":{"type":"tool.call","stepUuid":"step1","toolCallId":"call1","name":"Screenshot","args":{}}}
{"type":"context.append_loop_event","event":{"type":"tool.result","toolCallId":"call1","result":{"output":[{"type":"image_url","imageUrl":{"url":"data:image/png;base64,` + strings.Repeat("old-image", 20000) + `"}}]}}}
{"type":"context.append_loop_event","event":{"type":"step.end","stepUuid":"step1"}}
{"type":"tools.update_store","agentId":"main","key":"todo","value":{"next":"preserve"}}
{"type":"context.apply_compaction","summary":"summary","compactedCount":3,"keptUserMessageCount":1,"wireLines":{"start":1,"end":6}}
{"type":"context.append_message","message":{"role":"user","origin":{"kind":"injection"},"content":[{"type":"text","text":"fresh injection"}]}}
`)
}

func TestKimiCompactionKeepsUserSelectionAndEventPositions(t *testing.T) {
	data := kimiCompactionFixture()
	got := trimKimiTranscript(data)
	if len(got) > len(data)/20 || bytes.Contains(got, []byte("old-image")) {
		t.Fatal("old Kimi tool media survived")
	}
	if bytes.Count(got, []byte("\n")) != bytes.Count(data, []byte("\n")) {
		t.Fatal("wire line indices changed")
	}
	for _, token := range []string{"keep user", "call1", "preserve", "fresh injection", `"compactedCount":3`, `"wireLines":{"start":1,"end":6}`} {
		if !bytes.Contains(got, []byte(token)) {
			t.Fatal("lost state", token)
		}
	}
	for _, v := range []struct{ old, new string }{{`"keptUserMessageCount":1`, `"legacyTail":true`}, {`"summary":"summary"`, `"summary":null`}} {
		invalid := bytes.Replace(data, []byte(v.old), []byte(v.new), 1)
		if !bytes.Equal(trimKimiTranscript(invalid), invalid) {
			t.Fatal("unsupported Kimi checkpoint changed")
		}
	}
	rollback := append(append([]byte{}, data...), []byte(`{"type":"context.undo","count":1}`+"\n")...)
	if !bytes.Equal(trimKimiTranscript(rollback), rollback) {
		t.Fatal("Kimi undo trimmed")
	}
}

func TestOpenCodeCompactionKeepsRetainedTail(t *testing.T) {
	data := []byte(`{"info":{"id":"ses_test"},"messages":[
 {"info":{"id":"old","role":"user"},"parts":[{"type":"file","url":"` + strings.Repeat("old-image", 20000) + `"}]},
 {"info":{"id":"keep","role":"user"},"parts":[{"type":"text","text":"keep"}]},
 {"info":{"id":"boundary","role":"user"},"parts":[{"type":"compaction","tail_start_id":"keep"}]},
 {"info":{"id":"summary","role":"assistant","parentID":"boundary","summary":true,"finish":"stop"},"parts":[{"type":"text","text":"summary"}]},
 {"info":{"id":"after","role":"user"},"parts":[{"type":"text","text":"continue"}]}]}`)
	got := trimOpenCodeExport(data)
	var export struct {
		Messages []struct {
			Info struct {
				ID string `json:"id"`
			} `json:"info"`
		} `json:"messages"`
	}
	if json.Unmarshal(got, &export) != nil || len(export.Messages) != 4 || export.Messages[0].Info.ID != "keep" || len(got) > len(data)/20 {
		t.Fatal("incorrect OpenCode retained range")
	}
	for _, v := range []struct{ old, new string }{{`"tail_start_id":"keep"`, `"tail_start_id":"missing"`}, {`"finish":"stop"`, `"finish":null`}, {`"finish":"stop"`, `"finish":"stop","error":{"name":"failed"}`}, {`"info":{"id":"ses_test"}`, `"info":{"id":"ses_test","revert":{"messageID":"old"}}`}} {
		invalid := bytes.Replace(data, []byte(v.old), []byte(v.new), 1)
		if !bytes.Equal(trimOpenCodeExport(invalid), invalid) {
			t.Fatal("unsafe OpenCode trim", v)
		}
	}
}

func TestNativeSessionCollectionAndImport(t *testing.T) {
	for _, a := range []string{"pi", "kimi"} {
		t.Run(a, func(t *testing.T) {
			local, cloud := t.TempDir(), t.TempDir()
			cwd := "/Users/me/repo"
			id := conversationA
			var transcript string
			if a == "pi" {
				transcript = filepath.Join(local, "sessions", piSessionDirectory(cwd), "2026_"+id+".jsonl")
				writeFixture(t, transcript, string(piCompactionFixture()), time.Time{})
			} else {
				id = "session_" + id
				root := filepath.Join(local, "sessions", kimiWorkdirBucket(cwd), id)
				transcript = filepath.Join(root, "agents/main/wire.jsonl")
				state := map[string]any{"id": id, "version": 2, "cwd": cwd, "agents": map[string]any{"main": map[string]string{"homedir": filepath.Join(root, "agents/main")}}}
				writeFixture(t, filepath.Join(root, "state.json"), string(encodeAny(state)), time.Time{})
				writeFixture(t, transcript, string(kimiCompactionFixture()), time.Time{})
				writeFixture(t, filepath.Join(root, "attachments/keep.png"), "image bytes", time.Time{})
			}
			before, _ := os.ReadFile(transcript)
			b, args, err := CollectLocalSession(local, a, cwd, launchID, []string{"--continue"})
			if err != nil || b == nil || !reflect.DeepEqual(args, []string{"--session", id}) {
				t.Fatalf("collect: %v %v %v", b, args, err)
			}
			wire, err := EncodeSessionImport(b)
			if err != nil {
				t.Fatal(err)
			}
			n, err := ReceiveSessionImport(cloud, bytes.NewReader(wire))
			if err != nil || n != len(b.Files) {
				t.Fatalf("import: %d %v", n, err)
			}
			if !HasCloudSession(cloud, a, id, b.Marker) {
				t.Fatal("native session not discoverable")
			}
			// An existing cloud conversation is authoritative even if a newer local
			// edit changes its marker. Never clobber cloud-only turns.
			b.Marker = "different-local-tip"
			if n, err := ApplySessionImport(cloud, *b); n != 0 || err != nil {
				t.Fatalf("duplicate import: %d %v", n, err)
			}
			after, _ := os.ReadFile(transcript)
			if !bytes.Equal(before, after) {
				t.Fatal("local transcript changed")
			}
			matches := nativeSessionCandidates(filepath.Join(cloudAgentHome(cloud, a), "sessions"), a)
			if len(matches) != 1 || matches[0].cwd != filepath.Join(cloud, ".fen", launchID) {
				t.Fatalf("cloud directory mapping: %+v", matches)
			}
			if a == "kimi" {
				state, _ := os.ReadFile(matches[0].file)
				if bytes.Contains(state, []byte(local)) {
					t.Fatal("Kimi agent home not remapped")
				}
				index, _ := os.ReadFile(filepath.Join(cloudAgentHome(cloud, a), "session_index.jsonl"))
				if !bytes.Contains(index, []byte(id)) {
					t.Fatal("Kimi index missing")
				}
			}
		})
	}
}

func TestNativeResumeFlags(t *testing.T) {
	for _, tc := range []struct {
		agent      string
		args, want []string
	}{
		{"pi", []string{"--session-dir", "custom", "--continue", "--model", "foo"}, []string{"--session", conversationA, "--model", "foo"}},
		{"pi", []string{"--session=local.jsonl"}, []string{"--session", conversationA}},
		{"kimi", []string{"--session", "session_old"}, []string{"--session", conversationA}},
		{"opencode", []string{"-c"}, []string{"--session", conversationA}},
	} {
		r, ok := parseResume(tc.agent, tc.args)
		if !ok || !reflect.DeepEqual(r.rewrite(conversationA), tc.want) {
			t.Fatalf("%s: %+v", tc.agent, r)
		}
	}
	for _, args := range [][]string{{"--resume"}, {"--", "--continue"}, {"--model", "--continue"}, {"--no-session", "--continue"}} {
		if _, ok := parseResume("pi", args); ok {
			t.Fatal("unexpected resume", args)
		}
	}
}

func TestImportRejectsSymlinkDestinationAndDuplicateFiles(t *testing.T) {
	cloud, outside := t.TempDir(), t.TempDir()
	root := cloudAgentHome(cloud, "pi")
	os.MkdirAll(root, 0700)
	os.Symlink(outside, filepath.Join(root, "sessions"))
	b := SessionImport{Version: 1, Agent: "pi", Conversation: conversationA, LaunchID: launchID, Marker: "tip", Files: []SessionFile{{Path: conversationA + ".jsonl", Data: piCompactionFixture()}}}
	if _, err := ApplySessionImport(cloud, b); err == nil {
		t.Fatal("followed destination symlink")
	}
	b.Files = append(b.Files, b.Files[0])
	if b.validate() == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestKimiNativeJournalRestore(t *testing.T) {
	source := os.Getenv("FENNEC_KIMI_BUNDLE")
	if source == "" {
		t.Skip("set FENNEC_KIMI_BUNDLE to the installed Kimi main.mjs")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	end := bytes.LastIndex(data, []byte("// src/main.ts\n"))
	if end < 0 {
		t.Fatal("unrecognized native bundle entry point")
	}
	// Expose the installed journal loader from a temporary copy. Do not execute
	// its CLI main, initialize providers or touch any user session/configuration.
	module := filepath.Join(t.TempDir(), "kimi-loader.mjs")
	if err := os.Symlink(filepath.Join(filepath.Dir(filepath.Dir(source)), "node_modules"), filepath.Join(filepath.Dir(module), "node_modules")); err != nil {
		t.Fatal(err)
	}
	copy := append(append([]byte{}, data[:end]...), []byte("init_journal(); export { restoreLoopJournal };\n")...)
	if err := os.WriteFile(module, copy, 0600); err != nil {
		t.Fatal(err)
	}
	script := `import fs from 'node:fs'; import {pathToFileURL} from 'node:url';
 const m=await import(pathToFileURL(process.argv[1]));
 const load=p=>m.restoreLoopJournal(fs.readFileSync(p,'utf8').trim().split('\n').map(JSON.parse));
 const a=JSON.stringify(load(process.argv[2])), b=JSON.stringify(load(process.argv[3]));
 if(a!==b){console.error(a+'\n'+b);process.exit(1)}`
	fixtures := [][]byte{kimiCompactionFixture()}
	second := append(append([]byte{}, fixtures[0]...), []byte(`{"type":"context.append_message","message":{"role":"assistant","content":[{"type":"text","text":"second answer"}],"toolCalls":[]}}
{"type":"context.apply_compaction","summary":"second summary","compactedCount":4,"keptUserMessageCount":1}
{"type":"context.append_message","message":{"role":"user","content":[{"type":"text","text":"latest request"}],"toolCalls":[]}}
`)...)
	fixtures = append(fixtures, second)
	for i, fixture := range fixtures {
		before, after := filepath.Join(t.TempDir(), "before.jsonl"), filepath.Join(t.TempDir(), "after.jsonl")
		os.WriteFile(before, fixture, 0600)
		os.WriteFile(after, trimKimiTranscript(fixture), 0600)
		cmd := exec.Command("node", "--input-type=module", "-e", script, module, before, after)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native Kimi restore %d: %v %s", i, err, output)
		}
	}
}

func TestOpenCodeNativeExportImport(t *testing.T) {
	binary := os.Getenv("FENNEC_OPENCODE_BINARY")
	if binary == "" {
		t.Skip("set FENNEC_OPENCODE_BINARY to an installed original OpenCode executable")
	}
	t.Setenv("PATH", filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	home, workspace := t.TempDir(), t.TempDir()
	root := filepath.Join(home, "opencode", "data")
	cli, err := newOpenCodeSessionCLI(root, workspace, binary)
	if err != nil {
		t.Fatal(err)
	}
	if cli.v2 {
		t.Skip("v1 schema fixture; v2 checkpoints have separate unit coverage")
	}
	id := "ses_fennecCompactionTest"
	messages := []any{}
	for i, name := range []string{"old", "keep", "boundary", "summary", "after"} {
		mid := "msg_" + name
		info := map[string]any{"id": mid, "sessionID": id, "role": "user", "time": map[string]any{"created": 1000 + i}, "agent": "build", "model": map[string]string{"providerID": "test", "modelID": "test"}}
		part := map[string]any{"id": "prt_" + name, "messageID": mid, "sessionID": id, "type": "text", "text": name}
		if name == "old" {
			part["text"] = strings.Repeat("obsolete screenshot", 10000)
		}
		if name == "boundary" {
			part = map[string]any{"id": "prt_boundary", "messageID": mid, "sessionID": id, "type": "compaction", "auto": true, "tail_start_id": "msg_keep"}
		}
		if name == "summary" {
			info = map[string]any{"id": mid, "sessionID": id, "role": "assistant", "parentID": "msg_boundary", "summary": true, "finish": "stop", "time": map[string]int{"created": 1003, "completed": 1004}, "providerID": "test", "modelID": "test", "mode": "build", "agent": "build", "path": map[string]string{"cwd": workspace, "root": workspace}, "cost": 0, "tokens": map[string]any{"input": 1, "output": 1, "reasoning": 0, "cache": map[string]int{"read": 0, "write": 0}}}
		}
		messages = append(messages, map[string]any{"info": info, "parts": []any{part}})
	}
	export := encodeAny(map[string]any{"info": map[string]any{"id": id, "slug": "test", "projectID": "global", "directory": workspace, "title": "Fennec compaction fixture", "version": "1.18.18", "time": map[string]int{"created": 1000, "updated": 2000}}, "messages": messages})
	file := filepath.Join(t.TempDir(), "fixture.json")
	os.WriteFile(file, export, 0600)
	if out, err := cli.run("import", file); err != nil || !bytes.Contains(out, []byte("Imported session:")) {
		t.Fatalf("native fixture import: %s %v", out, err)
	}
	b, args, err := CollectLocalSession(root, "opencode", workspace, launchID, []string{"--continue"}, binary)
	if err != nil || b == nil {
		t.Fatalf("native collection: %v", err)
	}
	if len(b.Files[0].Data) > len(export)/20 || !reflect.DeepEqual(args, []string{"--session", id}) {
		t.Fatal("native export was not trimmed or resolved")
	}
	cloud := t.TempDir()
	if _, err := ApplySessionImport(cloud, *b); err != nil {
		t.Fatal("native cloud import", err)
	}
	destination, err := newOpenCodeSessionCLI(cloudAgentHome(cloud, "opencode"), filepath.Join(cloud, ".fen", launchID), binary)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := destination.export(id)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Messages []json.RawMessage `json:"messages"`
		Info     struct {
			Directory string `json:"directory"`
		} `json:"info"`
	}
	expected, _ := filepath.EvalSymlinks(filepath.Join(cloud, ".fen", launchID))
	if json.Unmarshal(restored, &parsed) != nil || len(parsed.Messages) != 4 || parsed.Info.Directory != expected {
		t.Fatalf("native restored export: %s", restored)
	}
	if n, err := ApplySessionImport(cloud, *b); n != 0 || err != nil {
		t.Fatalf("duplicate native import: %d %v", n, err)
	}
}

func TestKimiNativeSessionDiscovery(t *testing.T) {
	binary := os.Getenv("FENNEC_KIMI_BINARY")
	if binary == "" {
		t.Skip("set FENNEC_KIMI_BINARY to the installed Kimi executable")
	}
	cloud := t.TempDir()
	workspace := filepath.Join(cloud, ".fen", launchID)
	os.MkdirAll(workspace, 0700)
	id := "session_" + conversationA
	state := map[string]any{"id": id, "version": 2, "cwd": "/local", "createdAt": 1000, "updatedAt": 2000, "archived": false, "title": "Fennec fixture", "titleKind": "generated", "isCustomTitle": false, "lastPrompt": "keep user", "lastTurnReason": "completed", "agents": map[string]any{"main": map[string]any{"homedir": "/local/sessions/bucket/" + id + "/agents/main", "type": "main", "parentAgentId": nil}}}
	b := SessionImport{Version: 1, Agent: "kimi", Conversation: id, LaunchID: launchID, Marker: "tip", Files: []SessionFile{{Path: "state.json", Data: encodeAny(state)}, {Path: "agents/main/wire.jsonl", Data: trimKimiTranscript(kimiCompactionFixture())}}}
	if _, err := ApplySessionImport(cloud, b); err != nil {
		t.Fatal(err)
	}
	root := cloudAgentHome(cloud, "kimi")
	cmd := exec.Command(binary, "session", "list", "--all", "--json")
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(), "KIMI_CODE_HOME="+root)
	output, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte(id)) {
		t.Fatalf("native Kimi session discovery: %v %s", err, output)
	}
	// Recover publication interrupted between the directory rename and index
	// append; preserve the already-published conversation while repairing lookup.
	if err := os.Remove(filepath.Join(root, "session_index.jsonl")); err != nil {
		t.Fatal(err)
	}
	b.Files[1].Data = []byte("must not overwrite")
	if _, err := ApplySessionImport(cloud, b); err != nil {
		t.Fatal(err)
	}
	if !HasCloudSession(cloud, "kimi", id, "tip") {
		t.Fatal("index was not repaired")
	}
	copies := nativeSessionCandidates(filepath.Join(root, "sessions"), "kimi")
	wire, _ := os.ReadFile(filepath.Join(filepath.Dir(copies[0].file), "agents/main/wire.jsonl"))
	if bytes.Contains(wire, []byte("must not overwrite")) {
		t.Fatal("repair overwrote cloud history")
	}
}

func TestClaudeImportPublishesTranscriptAfterSidecars(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	project := filepath.Join(cloudAgentHome(home, "claude"), "projects", ClaudeProjectDirectory(filepath.Join(home, ".fen", launchID)))
	os.MkdirAll(filepath.Join(project, conversationA), 0700)
	os.Symlink(outside, filepath.Join(project, conversationA, "subagents"))
	b := SessionImport{Version: 1, Agent: "claude", Conversation: conversationA, LaunchID: launchID, Marker: "tip", Files: []SessionFile{{Path: conversationA + ".jsonl", Data: []byte(`{"uuid":"tip"}` + "\n")}, {Path: conversationA + "/subagents/a.jsonl", Data: []byte("{}\n")}}}
	if _, err := ApplySessionImport(home, b); err == nil {
		t.Fatal("unsafe sidecar accepted")
	}
	if HasCloudSession(home, "claude", conversationA, "tip") {
		t.Fatal("failed import published a partial conversation")
	}
}

func TestKimiContinueUsesNativeUpdatedTime(t *testing.T) {
	root := t.TempDir()
	cwd := "/repo"
	for i, id := range []string{conversationA, conversationB} {
		updated := 2000 - i*1000
		state := map[string]any{"id": "session_" + id, "cwd": cwd, "updatedAt": updated}
		file := filepath.Join(root, "sessions", kimiWorkdirBucket(cwd), "session_"+id, "state.json")
		writeFixture(t, file, string(encodeAny(state)), time.Now().Add(time.Duration(i)*time.Hour))
	}
	b, _, err := CollectLocalSession(root, "kimi", cwd, launchID, []string{"-c"})
	if err != nil || b == nil || b.Conversation != "session_"+conversationA {
		t.Fatalf("native ordering: %+v %v", b, err)
	}
}
