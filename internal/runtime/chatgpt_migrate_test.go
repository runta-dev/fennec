package runtime

import (
	"bytes"
	"fennec/internal/localssh"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestCloudRolloutMovesWorkingDirectoriesOnly(t *testing.T) {
	home := t.TempDir()
	filter := &chatgptPathFilter{home: home, device: "1234abcd", paths: map[string]string{}}
	rollout := []byte(`{"type":"session_meta","payload":{"id":"x","cwd":"/Users/me/project"}}
{"type":"response_item","payload":{"text":"keeps \"cwd\" text and   spacing"}}
{"type":"turn_context","payload":{"cwd":"/Users/me/project","model":"m"}}
`)
	got := filter.cloudRollout(rollout)
	lines := bytes.Split(got, []byte("\n"))
	if len(lines) != 4 || !bytes.Equal(lines[1], bytes.Split(rollout, []byte("\n"))[1]) {
		t.Fatal(string(got))
	}
	if bytes.Contains(got, []byte(`"cwd":"/Users/me/project"`)) || strings.Count(string(got), filepath.Join(home, ".fen")) != 2 {
		t.Fatal(string(got))
	}
}

func TestMigrationSkipsThreadsAlreadyInTheCloud(t *testing.T) {
	codexHome := t.TempDir()
	id := "01a1c8a1-e6c4-72e0-af13-faee883ee4c7"
	day := filepath.Join(codexHome, "sessions/2026/11/11")
	os.MkdirAll(day, 0700)
	os.WriteFile(filepath.Join(day, "rollout-2026-11-11T11-23-36-"+id+".jsonl"), []byte("{}\n"), 0600)
	if path, err := localssh.FindCodexRollout(codexHome, id); err != nil || !strings.HasSuffix(path, id+".jsonl") {
		t.Fatal(path, err)
	}
	// No device: a missing thread is left alone instead of failing the request.
	filter := &chatgptPathFilter{home: t.TempDir(), codexHome: codexHome}
	attempted := func(thread string) bool { _, ok := migrationAttempts.Load(codexHome + "\x00" + thread); return ok }
	filter.migrateThreads([]byte(`{"id":1,"method":"thread/resume","params":{"threadId":"` + id + `"}}`))
	other := "01a1c8a1-0000-0000-0000-000000000000"
	// Sidebar requests naming many threads never start a move.
	filter.migrateThreads([]byte(`{"id":2,"method":"thread/attachment/list","params":{"threadId":"` + other + `"}}`))
	if attempted(other) {
		t.Fatal("sidebar request triggered a migration")
	}
	filter.migrateThreads([]byte(`{"id":3,"method":"thread/read","params":{"threadId":"` + other + `"}}`))
	// A new connection does not retry a thread already checked.
	again := &chatgptPathFilter{home: t.TempDir(), codexHome: codexHome, device: "1234abcd"}
	again.migrateThreads([]byte(`{"id":4,"method":"thread/resume","params":{"threadId":"` + other + `"}}`))
	if !attempted(id) || !attempted(other) {
		t.Fatal("attempts not recorded")
	}
}

func TestRolloutNamesIncludeCompressedAndChildThreads(t *testing.T) {
	id := "01a19bbb-61bb-78c0-b816-27926e999fd4"
	for name, want := range map[string]bool{
		"rollout-2026-11-02T18-08-31-" + id + ".jsonl.zst":                                      true,
		"rollout-2026-11-02T18-08-31-" + id + ".jsonl":                                          true,
		"rollout-2026-11-02T20-43-02-01a19c48-db7e-7d70-aad6-4c5056f146c4_" + id + ".jsonl.zst": true,
		"rollout-2026-11-02T20-43-02-" + id + "_01a19c48-db7e-7d70-aad6-4c5056f146c4.jsonl.zst": false,
		"notes-" + id + ".jsonl": false,
	} {
		if localssh.IsCodexRolloutName(name, id) != want {
			t.Fatal(name)
		}
	}
}

func TestCompressedRolloutKeepsCodexFormat(t *testing.T) {
	filter := &chatgptPathFilter{home: t.TempDir(), device: "1234abcd", paths: map[string]string{}}
	plain := []byte(`{"type":"session_meta","payload":{"cwd":"/Users/me/p"}}` + "\n")
	encoder, _ := zstd.NewWriter(nil)
	compressed := encoder.EncodeAll(plain, nil)
	got, err := filter.cloudRolloutFile("rollout-x-id.jsonl.zst", compressed)
	if err != nil {
		t.Fatal(err)
	}
	decoder, _ := zstd.NewReader(nil)
	decoded, err := decoder.DecodeAll(got, nil)
	if err != nil || bytes.Contains(decoded, []byte("/Users/me/p")) || !bytes.Contains(decoded, []byte(".fen")) {
		t.Fatal(string(decoded), err)
	}
}
