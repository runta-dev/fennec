package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func cursorTestDB(t *testing.T, file string) (*sql.DB, map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := openCursorDB(file, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA user_version=1; CREATE TABLE blobs(id TEXT PRIMARY KEY,data BLOB); CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT)"); err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	put := func(data []byte) []byte {
		s := sha256.Sum256(data)
		id := hex.EncodeToString(s[:])
		blobs[id] = data
		if _, err := db.Exec("INSERT INTO blobs VALUES(?,?)", id, data); err != nil {
			t.Fatal(err)
		}
		return s[:]
	}
	keep := put([]byte("retained screenshot"))
	summary := put([]byte("latest summary"))
	root := put(append(append([]byte{0x0a, 32}, keep...), append([]byte{0x32, 32}, summary...)...))
	put([]byte(strings.Repeat("obsolete screenshot", 100000)))
	meta := hex.EncodeToString(encodeAny(map[string]any{"agentId": conversationA, "latestRootBlobId": hex.EncodeToString(root), "createdAt": 1234, "name": "fixture", "mode": "default"}))
	if _, err := db.Exec("INSERT INTO meta VALUES('0',?)", meta); err != nil {
		t.Fatal(err)
	}
	return db, blobs
}

func TestCursorSnapshotRetainsReachableGraphAndLiveWAL(t *testing.T) {
	local, cloud := t.TempDir(), t.TempDir()
	cwd := "/Users/me/repo"
	file := filepath.Join(local, "chats", cursorWorkspaceBucket(cwd), conversationA, "store.db")
	db, blobs := cursorTestDB(t, file)
	defer db.Close()
	b, args, err := CollectLocalSession(local, "cursor", cwd, launchID, []string{"--continue"})
	if err != nil || b == nil {
		t.Fatalf("collect: %v", err)
	}
	if !reflect.DeepEqual(args, []string{"--resume", conversationA}) || len(b.Files[0].Data) > 100000 {
		t.Fatal("not compacted/resolved")
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM blobs").Scan(&count); err != nil || count != 4 {
		t.Fatalf("source mutated: %d %v", count, err)
	}
	wire, err := EncodeSessionImport(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ReceiveSessionImport(cloud, bytes.NewReader(wire)); err != nil {
		t.Fatal(err)
	}
	if !HasCloudSession(cloud, "cursor", conversationA, b.Marker) {
		t.Fatal("not found")
	}
	copies := vendorCandidates(cloudAgentHome(cloud, "cursor"), "cursor")
	restored, err := openCursorDB(filepath.Join(copies[0].dir, "store.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	rows, err := restored.Query("SELECT id,data FROM blobs")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var id string
		var data []byte
		if err = rows.Scan(&id, &data); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, blobs[id]) {
			t.Fatal("retained blob changed")
		}
		n++
	}
	rows.Close()
	if n != 3 {
		t.Fatalf("retained %d blobs", n)
	}
	b.Marker = "new-local-tip"
	if n, err := ApplySessionImport(cloud, *b); err != nil || n != 0 {
		t.Fatalf("overwrote cloud: %d %v", n, err)
	}
}

func TestCursorUnknownSchemaAndMutableIDsTravelWhole(t *testing.T) {
	for _, mutation := range []string{"PRAGMA user_version=2", "CREATE TABLE future_state(root TEXT)", "UPDATE blobs SET id='mutable-id' WHERE length(data)>100000"} {
		t.Run(mutation, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "store.db")
			db, _ := cursorTestDB(t, file)
			defer db.Close()
			if _, err := db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			data, err := snapshotCursorSession(file, conversationA)
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "store.db")
			os.WriteFile(out, data, 0600)
			copy, err := openCursorDB(out, true)
			if err != nil {
				t.Fatal(err)
			}
			defer copy.Close()
			var count int
			if err = copy.QueryRow("SELECT count(*) FROM blobs").Scan(&count); err != nil || count != 4 {
				t.Fatalf("unsafe cut: %d %v", count, err)
			}
		})
	}
}

func grokFixture() []SessionFile {
	envelope := func(method, kind string, fields map[string]any) []byte {
		fields["sessionUpdate"] = kind
		return append(encodeAny(map[string]any{"timestamp": 1234, "method": method, "params": map[string]any{"sessionId": conversationA, "update": fields}}), '\n')
	}
	update := envelope("session/update", "user_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": strings.Repeat("old screenshot", 10000)}})
	update = append(update, envelope("_x.ai/session/update", "compaction_checkpoint", map[string]any{"checkpoint_id": "cp", "checkpoint_file": "compaction_checkpoints/cp.json", "schema_version": 1, "prompt_index_at_compaction": 4, "created_at": "2026-01-01T00:00:00Z"})...)
	update = append(update, envelope("session/update", "agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "retained answer"}})...)
	summary := encodeAny(map[string]any{"info": map[string]string{"id": conversationA, "cwd": "/Users/me/repo"}, "session_summary": "Fennec fixture", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z", "num_messages": 5, "num_chat_messages": 2, "current_model_id": "test-model", "chat_format_version": 0})
	chat := []byte("{\"role\":\"user\",\"content\":\"latest summary\"}\n{\"role\":\"assistant\",\"content\":\"retained answer\"}\n")
	cp := encodeAny(map[string]any{"checkpoint_id": "cp", "schema_version": 1, "prompt_index_at_compaction": 4, "compacted_history": []any{map[string]string{"type": "system", "content": "latest summary"}}, "created_at": "2026-01-01T00:00:00Z"})
	return []SessionFile{{"summary.json", summary}, {"chat_history.jsonl", chat}, {"updates.jsonl", update}, {"compaction_checkpoints/cp.json", cp}, {"plan.json", []byte(`{"todos":[]}`)}}
}

func TestGrokCompactionAndImport(t *testing.T) {
	local, cloud := t.TempDir(), t.TempDir()
	dir := filepath.Join(local, "sessions", "%2FUsers%2Fme%2Frepo", conversationA)
	files := grokFixture()
	for _, f := range files {
		writeFixture(t, filepath.Join(dir, f.Path), string(f.Data), time.Time{})
	}
	b, args, err := CollectLocalSession(local, "grok", "/Users/me/repo", launchID, []string{"-c"})
	if err != nil || b == nil {
		t.Fatalf("collect: %v", err)
	}
	if !reflect.DeepEqual(args, []string{"--resume", conversationA}) {
		t.Fatal(args)
	}
	for _, f := range b.Files {
		if f.Path == "updates.jsonl" && (bytes.Contains(f.Data, []byte("old screenshot")) || !bytes.Contains(f.Data, []byte("retained answer"))) {
			t.Fatal("wrong compaction boundary")
		}
	}
	if _, err = ApplySessionImport(cloud, *b); err != nil {
		t.Fatal(err)
	}
	copies := vendorCandidates(cloudAgentHome(cloud, "grok"), "grok")
	if len(copies) != 1 || copies[0].cwd != filepath.Join(cloud, ".fen", launchID) {
		t.Fatalf("invalid cloud discovery: %+v", copies)
	}
	chat, _ := os.ReadFile(filepath.Join(copies[0].dir, "chat_history.jsonl"))
	if !bytes.Equal(chat, files[1].Data) {
		t.Fatal("active context changed")
	}
	before, _ := os.ReadFile(filepath.Join(dir, "updates.jsonl"))
	if !bytes.Equal(before, files[2].Data) {
		t.Fatal("source changed")
	}
	if n, err := ApplySessionImport(cloud, *b); n != 0 || err != nil {
		t.Fatalf("duplicate import: %d %v", n, err)
	}
}

func TestGrokUnsafeCheckpointsTravelWhole(t *testing.T) {
	for _, kind := range []string{"missing", "version", "rewind", "malformed", "index"} {
		t.Run(kind, func(t *testing.T) {
			files := grokFixture()
			switch kind {
			case "missing":
				files = append(files[:3], files[4:]...)
			case "version":
				files[3].Data = bytes.ReplaceAll(files[3].Data, []byte(`"schema_version":1`), []byte(`"schema_version":2`))
			case "index":
				files[3].Data = bytes.ReplaceAll(files[3].Data, []byte(`"prompt_index_at_compaction":4`), []byte(`"prompt_index_at_compaction":3`))
			case "rewind":
				files[2].Data = append(files[2].Data, []byte(`{"method":"_x.ai/session/update","params":{"update":{"sessionUpdate":"rewind_marker"}}}`)...)
			case "malformed":
				files[2].Data = append(files[2].Data, []byte("broken")...)
			}
			if !reflect.DeepEqual(files, trimGrokSession(files)) {
				t.Fatal("unsafe compaction")
			}
		})
	}
}

func TestVendorResumeArguments(t *testing.T) {
	for _, tc := range []struct {
		agent      string
		args       []string
		id         string
		latest, ok bool
	}{
		{"cursor", []string{"--resume", conversationA}, conversationA, false, true},
		{"cursor", []string{"--resume"}, "", false, false},
		{"cursor", []string{"resume"}, "", true, true},
		{"cursor", []string{"--resume=-1"}, "", true, true},
		{"grok", []string{"-r", "my session"}, "my session", false, true},
		{"grok", []string{"--resume"}, "", true, true},
		{"grok", []string{"--session-id", conversationA}, "", false, false},
		{"grok", []string{"--model", "--continue"}, "", false, false},
		{"cursor", []string{"--", "--continue"}, "", false, false},
	} {
		r, ok := parseResume(tc.agent, tc.args)
		if ok != tc.ok || r.id != tc.id || r.latest != tc.latest {
			t.Errorf("%s %v: %+v %v", tc.agent, tc.args, r, ok)
		}
	}
}

func TestGrokNativeSessionExport(t *testing.T) {
	binary := os.Getenv("FENNEC_GROK_BINARY")
	if binary == "" {
		t.Skip("set FENNEC_GROK_BINARY to the original Grok executable")
	}
	home := t.TempDir()
	files := grokFixture()
	b := SessionImport{Version: 1, Agent: "grok", LaunchID: launchID, Conversation: conversationA, Marker: "fixture", Files: trimGrokSession(files)}
	if _, err := ApplySessionImport(home, b); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "export", conversationA)
	cmd.Env = append(os.Environ(), "GROK_HOME="+cloudAgentHome(home, "grok"), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native export: %v %s", err, out)
	}
	if !bytes.Contains(out, []byte("retained answer")) || bytes.Contains(out, []byte("old screenshot")) {
		t.Fatalf("native projection: %s", out)
	}
}

func TestCursorNativeLoader(t *testing.T) {
	bundle := os.Getenv("FENNEC_CURSOR_BUNDLE")
	if bundle == "" {
		t.Skip("set FENNEC_CURSOR_BUNDLE to the installed Cursor index.js")
	}
	// Export the bundled loader instead of running the CLI entrypoint. Native
	// classes create and restore the fixture, with no login or inference.
	harness := `const fs=require('node:fs'),Module=require('node:module'),path=require('node:path'),crypto=require('node:crypto');
 const [file,dbfile,action,id]=process.argv.slice(1);let source=fs.readFileSync(file,'utf8');const entry='var __webpack_exports__=__webpack_require__("./src/main.tsx")';if(!source.includes(entry))throw Error('unknown bundle entry');source=source.replace(entry,'module.exports=__webpack_require__');const m=new Module(file,module);m.filename=file;m.paths=Module._nodeModulePaths(path.dirname(file));m._compile(source,file);const req=m.exports;
 (async()=>{await req.e(4945);const {MU:DB}=req('../cursor-sdk-local-runtime/dist/run-store/sqlite-blob-store.js'),{pH:Store}=req('../agent-kv/dist/agent-store.js');const proto=req('../proto/dist/generated/agent/v1/agent_pb.js');const db=await DB.initAndLoad(dbfile);const store=new Store(db,db);const ctx={};
 if(action==='create'){db.set('agentId',id);const summary=new proto.KRq({summary:'latest native summary'}).toBinary();const sum=crypto.createHash('sha256').update(summary).digest();await db.setBlob(ctx,sum,summary);const old=Buffer.from('obsolete screenshot'.repeat(100000));await db.setBlob(ctx,crypto.createHash('sha256').update(old).digest(),old);await store.handleCheckpoint(ctx,new proto.Y9z({summary:sum,messageCountAtLastCompaction:100}));}
 else{if(!await store.tryResetFromDb(ctx))throw Error('native restore failed');const state=store.getConversationStateStructure();const summary=proto.KRq.fromBinary(await db.getBlob(ctx,state.summary));process.stdout.write(JSON.stringify({state:state.toJson(),summary:summary.toJson()}));}
 await store.dispose();})().catch(e=>{console.error(e);process.exit(1)});`
	run := func(file, action string) []byte {
		t.Helper()
		cmd := exec.Command("node", "-e", harness, bundle, file, action, conversationA)
		out, err := cmd.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				t.Fatalf("native loader: %v %s", err, exit.Stderr)
			}
			t.Fatal(err)
		}
		return bytes.TrimSpace(out)
	}
	file := filepath.Join(t.TempDir(), "store.db")
	run(file, "create")
	before := run(file, "read")
	data, err := snapshotCursorSession(file, conversationA)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 100000 {
		t.Fatal("obsolete native blob retained")
	}
	copy := filepath.Join(t.TempDir(), "store.db")
	os.WriteFile(copy, data, 0600)
	after := run(copy, "read")
	var a, b any
	if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("native restore differs: %s / %s", before, after)
	}
}

func TestVendorContinueSkipsEmptyAndHiddenSessions(t *testing.T) {
	for _, kind := range []string{"cursor-empty", "cursor-subagent", "grok-hidden"} {
		t.Run(kind, func(t *testing.T) {
			local := t.TempDir()
			cwd := "/Users/me/repo"
			agent := "cursor"
			if kind == "grok-hidden" {
				agent = "grok"
				for _, id := range []string{conversationA, conversationB} {
					for _, f := range grokFixture() {
						data := bytes.ReplaceAll(f.Data, []byte(conversationA), []byte(id))
						if id == conversationB && f.Path == "summary.json" {
							var m map[string]any
							json.Unmarshal(data, &m)
							m["hidden"] = true
							m["updated_at"] = "2030-01-01T00:00:00Z"
							data = encodeAny(m)
						}
						writeFixture(t, filepath.Join(local, "sessions", "bucket", id, f.Path), string(data), time.Time{})
					}
				}
			} else {
				for _, id := range []string{conversationA, conversationB} {
					file := filepath.Join(local, "chats", cursorWorkspaceBucket(cwd), id, "store.db")
					db, _ := cursorTestDB(t, file)
					data, err := cursorMetadata(context.Background(), db, conversationA)
					if err != nil {
						t.Fatal(err)
					}
					var m map[string]any
					json.Unmarshal(data, &m)
					m["agentId"] = id
					if id == conversationB {
						if kind == "cursor-empty" {
							m["latestRootBlobId"] = ""
						} else {
							m["subagentInfo"] = map[string]string{"parentAgentId": conversationA}
						}
					}
					if _, err = db.Exec("UPDATE meta SET value=? WHERE key='0'", hex.EncodeToString(encodeAny(m))); err != nil {
						t.Fatal(err)
					}
					db.Close()
					if id == conversationB {
						future := time.Now().Add(time.Hour)
						os.Chtimes(file, future, future)
					}
				}
			}
			b, _, err := CollectLocalSession(local, agent, cwd, launchID, []string{"--continue"})
			if err != nil || b == nil || b.Conversation != conversationA {
				t.Fatalf("selected empty/hidden conversation: %v %v", b, err)
			}
		})
	}
}
