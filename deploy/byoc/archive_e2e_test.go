package byocimage

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestArchiveMovesToAnotherComputer moves queues and sessions from one
// self-hosted computer to another with `fen cloud export` and `fen cloud
// import` (THI-165), through the daemon and terminal service built from this
// source. Run it with make test-e2e-byoc-archive.
func TestArchiveMovesToAnotherComputer(t *testing.T) {
	image := os.Getenv("FENNEC_BYOC_IMAGE")
	if os.Getenv("FENNEC_BYOC_E2E") != "1" || image == "" {
		t.Skip("set FENNEC_BYOC_E2E=1 and FENNEC_BYOC_IMAGE to run the self-hosted end-to-end test")
	}
	root := t.TempDir()
	fen, daemonBinary, extraEnv := e2eBinaries(t, root)
	home, err := os.MkdirTemp("/tmp", "fe2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := os.MkdirAll(filepath.Join(home, ".config/fennec"), 0700); err != nil {
		t.Fatal(err)
	}
	h := &e2eHarness{t: t, root: root, client: fen, daemonBinary: daemonBinary, home: home, env: append(extraEnv,
		"HOME="+home,
		"USER=fennec-e2e",
		"FENNEC_CREDENTIALS_FILE="+filepath.Join(home, "runta-credentials.json"),
		"PATH="+filepath.Dir(fen)+":/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin",
		"LANG=en_US.UTF-8",
	)}
	h.startDaemon()
	t.Cleanup(h.stopDaemon)
	h.must("settings", "magpie", "off")
	var key struct {
		DeviceKey string `json:"device_key"`
	}
	if err := json.Unmarshal([]byte(h.must("cloud", "byoc", "key")), &key); err != nil {
		t.Fatal(err)
	}
	computer := func() (container, string) {
		volume := randomName("fennec-byoc-archive-")
		t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() })
		free, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := free.Addr().(*net.TCPAddr).Port
		free.Close()
		c, endpoint := startOn(t, image, volume, key.DeviceKey, port)
		fingerprint := docker(t, "exec", c.name, "fennec-cloud", "fingerprint")
		machine := docker(t, "exec", c.name, "fennec-cloud", "identity")
		h.must("cloud", "byoc", "pair", net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)), "--fingerprint", fingerprint)
		if state := h.waitReady(10 * time.Minute); state.Runtime.RuntimeID != machine {
			t.Fatalf("prepared %q, the container is %q", state.Runtime.RuntimeID, machine)
		}
		return c, machine
	}
	on := func(c container, script string) string {
		return docker(t, "exec", "-u", "fennec", c.name, "sh", "-c", script)
	}
	board := func(method, local, path string, body any) json.RawMessage {
		t.Helper()
		var out json.RawMessage
		params := map[string]any{"method": local, "path": path, "body": body}
		for i := 0; ; i++ {
			err := h.control(method, params, &out)
			if err == nil {
				return out
			}
			if i == 60 {
				t.Fatalf("%s %s: %v", local, path, err)
			}
			time.Sleep(time.Second)
		}
	}
	titles := func(raw json.RawMessage) []string {
		var requests []struct{ Title string }
		json.Unmarshal(raw, &requests)
		var out []string
		for _, r := range requests {
			out = append(out, r.Title)
		}
		return out
	}

	// The old computer has a queue, a knowledge entry and a session with a
	// conversation.
	old, oldMachine := computer()
	var project struct{ ID string }
	json.Unmarshal(board("board.call", "POST", "/v1/projects", map[string]any{"name": "Moving", "org": "e2e"}), &project)
	board("board.call", "POST", "/v1/projects/"+project.ID+"/requests", map[string]any{"author": "e2e", "title": "Made on the old computer", "tag": "bug", "body": "first"})
	on(old, `set -e
k="$HOME/.fen/board/projects/`+project.ID+`/knowledge/architecture"; mkdir -p "$k"; echo "remember this" > "$k/fact.md"
w="$HOME/.fen/brave-otter"; mkdir -p "$w/report" "$w/node_modules"; echo notes > "$w/report/notes.md"; echo x > "$w/node_modules/dep.js"
mkdir -p "$HOME/.local/fennec/launches"
echo '{"id":"brave-otter","agent":"claude","local_directory":"/","device":"e2e"}' > "$HOME/.local/fennec/launches/brave-otter.json"
p="$HOME/.local/fennec/native/claude/projects/-home-fennec--fen-brave-otter"; mkdir -p "$p"
printf '%s\n' '{"type":"user","cwd":"'"$w"'","uuid":"u1"}' > "$p/0f1e2d3c-4b5a-4968-8776-a5b4c3d2e1f0.jsonl"`)
	// The Mac's replica has the queue before the computer goes away.
	for i := 0; !strings.Contains(string(board("board.local.call", "GET", "/v1/projects", nil)), project.ID); i++ {
		if i == 60 {
			t.Fatal("the queue never reached the Mac replica")
		}
		time.Sleep(time.Second)
	}

	archive := filepath.Join(root, "moving.fenarchive")
	exported := time.Now()
	if path := strings.TrimSpace(h.must("cloud", "export", "--output", archive)); path != archive {
		t.Fatalf("exported to %q", path)
	}
	t.Logf("export in %s", time.Since(exported).Round(100*time.Millisecond))
	var manifest struct {
		Counts struct{ Requests, Sessions, Knowledge int }
	}
	json.Unmarshal([]byte(h.must("cloud", "inspect", archive)), &manifest)
	if manifest.Counts.Requests != 1 || manifest.Counts.Sessions != 1 || manifest.Counts.Knowledge != 1 {
		t.Fatalf("manifest %+v", manifest.Counts)
	}

	// Move to a new computer. Without an import, the Mac's queues stay bound
	// to the old one and the new computer has none.
	h.must("cloud", "byoc", "unpair", oldMachine)
	fresh, _ := computer()
	board("board.local.call", "POST", "/v1/projects/"+project.ID+"/requests", map[string]any{"author": "e2e", "title": "Made on the Mac after the export", "tag": "feature", "body": "second"})
	time.Sleep(6 * time.Second) // several sync steps
	if got := string(board("board.call", "GET", "/v1/projects", nil)); strings.Contains(got, project.ID) {
		t.Fatalf("the new computer has the queue before the import: %s", got)
	}
	if !strings.Contains(h.daemonLog(), "board is paired with a different account") {
		t.Log("expected the replica to refuse the new computer before the import")
	}

	imported := time.Now()
	var result struct {
		RequestsAdded       int `json:"requests_added"`
		KnowledgeAdded      int `json:"knowledge_added"`
		SessionsRestored    int `json:"sessions_restored"`
		ConversationsCopied int `json:"conversations_copied"`
	}
	if err := json.Unmarshal([]byte(h.must("cloud", "import", archive)), &result); err != nil {
		t.Fatal(err)
	}
	t.Logf("import in %s: %+v", time.Since(imported).Round(100*time.Millisecond), result)
	if result.RequestsAdded != 1 || result.KnowledgeAdded != 1 || result.SessionsRestored != 1 || result.ConversationsCopied != 1 {
		t.Fatalf("import result %+v", result)
	}
	if got := on(fresh, `cat "$HOME/.fen/board/projects/`+project.ID+`/knowledge/architecture/fact.md" "$HOME/.fen/brave-otter/report/notes.md"; test ! -e "$HOME/.fen/brave-otter/node_modules" && echo skipped`); got != "remember this\nnotes\nskipped" {
		t.Fatalf("restored files: %q", got)
	}
	if got := on(fresh, `grep -l '"cwd":"/home/fennec/.fen/brave-otter"' "$HOME"/.local/fennec/native/claude/projects/-home-fennec--fen-brave-otter/*.jsonl | wc -l`); got != "1" {
		t.Fatalf("conversation not restored where Claude looks: %q", got)
	}

	// The import rebinds the Mac's replica: the request made on the Mac after
	// the export reaches the new computer too.
	want := []string{"Made on the Mac after the export", "Made on the old computer"}
	for i := 0; ; i++ {
		got := titles(board("board.call", "GET", "/v1/projects/"+project.ID+"/requests", nil))
		if fmt.Sprint(sorted(got)) == fmt.Sprint(sorted(want)) {
			break
		}
		if i == 60 {
			t.Fatalf("requests on the new computer: %v\n%s", got, tail(h.daemonLog(), 4000))
		}
		time.Sleep(time.Second)
	}

	// Importing again adds nothing.
	json.Unmarshal([]byte(h.must("cloud", "import", archive)), &result)
	if result.RequestsAdded != 0 || result.SessionsRestored != 0 {
		t.Fatalf("second import %+v", result)
	}
}

func sorted(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
