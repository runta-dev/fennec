package terminalstream

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSessionImportUsesResidentCreditedStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	home := t.TempDir()
	serverInput, clientInput := io.Pipe()
	serverOutput, clientOutput := io.Pipe()
	defer clientInput.Close()
	defer serverOutput.Close()
	go ServeWithSockets(ctx, serverInput, clientOutput, nil, nil, nil, func(context.Context, cloudruntime.LocalOperation) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}, func(ctx context.Context, target string) (net.Conn, error) {
		if target != SocketSessionImport {
			t.Error("unexpected socket target", target)
		}
		return cloudruntime.DialSessionImport(ctx, home), nil
	})
	transport := startTransport(clientInput, serverOutput, func() error { return nil }, func() error { return nil }, nil)
	defer transport.Close()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	id := "af4ac027-381a-48d8-9cbc-32ba6f82c5fb"
	random := make([]byte, 6*Window)
	rand.Read(random)
	transcript := []byte(`{"type":"session","version":3,"id":"` + id + `","cwd":"/local"}` + "\n" + `{"type":"message","id":"u1","parentId":null,"message":{"role":"user","content":"` + base64.StdEncoding.EncodeToString(random) + `"}}` + "\n")
	bundle := &cloudruntime.SessionImport{Version: 1, Agent: "pi", LaunchID: "brisk-falcon", Conversation: id, Marker: "tip", Files: []cloudruntime.SessionFile{{Path: id + ".jsonl", Data: transcript}}}
	payload, _ := cloudruntime.EncodeSessionImport(bundle)
	if len(payload) < 3*Window {
		t.Fatal("fixture must cross the credit window")
	}
	done := make(chan error, 1)
	go func() { done <- ImportSession(ctx, transport, bundle) }()
	peer, err := transport.Launch(Frame{Type: "operation", ID: strings.Repeat("a", 32), Operation: &cloudruntime.LocalOperation{Kind: "list"}})
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := peer.Next(ctx); err != nil || frame.Type != "result" {
		t.Fatalf("peer operation: %v %v", frame, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !cloudruntime.HasCloudSession(home, "pi", id, "tip") {
		t.Fatal("import was not applied")
	}
	// A second import performs only the presence handshake. Invalid replacement
	// data would fail if it were uploaded/applied over the continued cloud copy.
	bundle.Files[0].Data = []byte("invalid replacement")
	if err := ImportSession(ctx, transport, bundle); err != nil {
		t.Fatal(err)
	}
	found := false
	filepath.WalkDir(home, func(file string, e os.DirEntry, err error) error {
		if err == nil && e.Name() == id+".jsonl" {
			data, _ := os.ReadFile(file)
			found = bytes.Contains(data, []byte(base64.StdEncoding.EncodeToString(random)))
		}
		return nil
	})
	if !found {
		t.Fatal("cloud conversation was overwritten")
	}
}

func TestSessionImportSocketRejectsTruncatedAndInvalidPayloads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn := cloudruntime.DialSessionImport(ctx, t.TempDir())
	defer conn.Close()
	go conn.Write([]byte(`{"agent":"pi","conversation":"../escape","marker":"tip"}` + "\n"))
	var reply cloudruntime.SessionImportReply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil || reply.Error == "" {
		t.Fatalf("invalid query: %+v %v", reply, err)
	}
}

func TestSessionImportResidentProcess(t *testing.T) {
	binary := os.Getenv("FENNEC_TERMINAL_BINARY")
	if binary == "" {
		t.Skip("set FENNEC_TERMINAL_BINARY to the freshly built terminal service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	home := t.TempDir()
	cmd := exec.CommandContext(ctx, binary, "serve", "--protocol="+strconv.Itoa(ProtocolVersion))
	cmd.Env = append(os.Environ(), "HOME="+home)
	transport, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	id := "af4ac027-381a-48d8-9cbc-32ba6f82c5fb"
	cursorFile := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", cursorFile)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT); CREATE TABLE blobs(id TEXT PRIMARY KEY,data BLOB)")
	if err == nil {
		_, err = db.Exec("INSERT INTO meta VALUES('0', ?)", hex.EncodeToString([]byte(`{"agentId":"`+id+`","latestRootBlobId":""}`)))
	}
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	cursorData, err := os.ReadFile(cursorFile)
	if err != nil {
		t.Fatal(err)
	}
	fixtures := map[string][]cloudruntime.SessionFile{
		"claude": {{Path: id + ".jsonl", Data: []byte(`{"type":"user","uuid":"tip","cwd":"/local","message":"fixture"}` + "\n")}},
		"codex":  {{Path: "sessions/2026/11/14/rollout-2026-11-14T10-00-00-" + id + ".jsonl", Data: []byte(`{"type":"session_meta","payload":{"id":"` + id + `","cwd":"/local"}}` + "\n")}},
		"pi":     {{Path: id + ".jsonl", Data: []byte(`{"type":"session","version":3,"id":"` + id + `","cwd":"/local"}` + "\n")}},
		"kimi":   {{Path: "state.json", Data: []byte(`{"version":2,"id":"session_` + id + `","cwd":"/local"}`)}, {Path: "agents/main/wire.jsonl", Data: []byte(`{"type":"metadata","protocol_version":"1.0"}` + "\n")}},
		"cursor": {{Path: "store.db", Data: cursorData}},
		"grok":   {{Path: "summary.json", Data: []byte(`{"info":{"id":"` + id + `","cwd":"/local"}}`)}, {Path: "chat_history.jsonl", Data: []byte(`{"type":"system","content":"fixture"}` + "\n")}},
	}
	for agent, files := range fixtures {
		conversation := id
		if agent == "kimi" {
			conversation = "session_" + id
		}
		b := &cloudruntime.SessionImport{Version: 1, Agent: agent, Conversation: conversation, LaunchID: "brisk-falcon", Marker: "tip", Files: files}
		if err := ImportSession(ctx, transport, b); err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
		if !cloudruntime.HasCloudSession(home, agent, conversation, "tip") {
			t.Fatal("process did not publish", agent)
		}
	}
}
