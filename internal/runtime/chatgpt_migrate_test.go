package runtime

import (
	"bytes"
	"fennec/internal/localssh"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	filter := &chatgptPathFilter{home: t.TempDir(), codexHome: codexHome, threads: map[string]bool{}}
	request := []byte(`{"id":1,"method":"thread/resume","params":{"threadId":"` + id + `"}}`)
	filter.migrateThreads(request)
	missing := []byte(`{"id":2,"method":"thread/read","params":{"threadId":"01a1c8a1-0000-0000-0000-000000000000"}}`)
	filter.migrateThreads(missing)
	filter.migrateThreads(missing)
	if len(filter.threads) != 2 {
		t.Fatal(filter.threads)
	}
}
