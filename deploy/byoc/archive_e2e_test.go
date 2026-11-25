package byocimage

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	computer := func() (container, string) {
		// Unpairing forgets the device key, so each computer authorizes the
		// current one.
		var key struct {
			DeviceKey string `json:"device_key"`
		}
		if err := json.Unmarshal([]byte(h.must("cloud", "byoc", "key")), &key); err != nil {
			t.Fatal(err)
		}
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
	// boardCall runs a board request on the computer itself. The Mac reaches
	// the board through the resident board socket, which a computer without
	// systemd does not serve yet (the queue service cannot be installed), so
	// queue data is seeded and checked in place.
	boardCall := func(c container, method, path, body string) string {
		t.Helper()
		return docker(t, "exec", "-i", "-u", "fennec", c.name, "sh", "-c", `printf '%s' "$1" | "$HOME/.local/fennec/bin/fennec-terminal" board-call `+method+" "+path, "sh", body)
	}
	paired := func() string {
		data, _ := os.ReadFile(filepath.Join(home, ".fen/board/paired-account"))
		return string(data)
	}
	waitPaired := func(not string) string {
		t.Helper()
		for i := 0; ; i++ {
			if got := paired(); got != "" && got != not {
				return got
			}
			if i == 60 {
				t.Fatalf("the Mac replica was not bound to the computer (still %q)", paired())
			}
			time.Sleep(time.Second)
		}
	}

	// The old computer has a queue, a knowledge entry and a session with a
	// conversation.
	old, oldMachine := computer()
	var project struct{ ID string }
	if err := json.Unmarshal([]byte(boardCall(old, "POST", "/v1/projects", `{"name":"Moving","org":"e2e","workers":[{"agent":"claude","on":true}]}`)), &project); err != nil || project.ID == "" {
		t.Fatalf("create queue: %v", err)
	}
	boardCall(old, "POST", "/v1/projects/"+project.ID+"/requests", `{"author":"e2e","title":"Made on the old computer","tag":"bug","body":"first"}`)
	on(old, `set -e
k="$HOME/.fen/board/projects/`+project.ID+`/knowledge/architecture"; mkdir -p "$k"; echo "remember this" > "$k/fact.md"
w="$HOME/.fen/brave-otter"; mkdir -p "$w/report" "$w/node_modules"; echo notes > "$w/report/notes.md"; echo x > "$w/node_modules/dep.js"
mkdir -p "$HOME/.local/fennec/launches"
echo '{"id":"brave-otter","agent":"claude","local_directory":"/","device":"e2e"}' > "$HOME/.local/fennec/launches/brave-otter.json"
p="$HOME/.local/fennec/native/claude/projects/-home-fennec--fen-brave-otter"; mkdir -p "$p"
printf '%s\n' '{"type":"user","cwd":"'"$w"'","uuid":"u1"}' > "$p/0f1e2d3c-4b5a-4968-8776-a5b4c3d2e1f0.jsonl"`)
	oldIdentity := waitPaired("")

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

	// Move to a new computer. Without an import the Mac's replica stays bound
	// to the old one.
	h.must("cloud", "byoc", "unpair", oldMachine)
	fresh, _ := computer()
	time.Sleep(6 * time.Second) // several replication steps
	if paired() != oldIdentity {
		t.Fatal("the Mac replica switched computers without an import")
	}
	if got := boardCall(fresh, "GET", "/v1/projects", ""); strings.Contains(got, project.ID) {
		t.Fatalf("the new computer has the queue before the import: %s", got)
	}

	imported := time.Now()
	var result struct {
		RequestsAdded       int `json:"requests_added"`
		RequestsExisting    int `json:"requests_existing"`
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
	if got := boardCall(fresh, "GET", "/v1/projects/"+project.ID+"/requests", ""); !strings.Contains(got, "Made on the old computer") {
		t.Fatalf("requests on the new computer: %s", got)
	}
	if got := on(fresh, `cat "$HOME/.fen/board/projects/`+project.ID+`/knowledge/architecture/fact.md" "$HOME/.fen/brave-otter/report/notes.md"; test ! -e "$HOME/.fen/brave-otter/node_modules" && echo skipped`); got != "remember this\nnotes\nskipped" {
		t.Fatalf("restored files: %q", got)
	}
	if got := on(fresh, `grep -l '"cwd":"/home/fennec/.fen/brave-otter"' "$HOME"/.local/fennec/native/claude/projects/-home-fennec--fen-brave-otter/*.jsonl | wc -l`); got != "1" {
		t.Fatalf("conversation not restored where Claude looks: %q", got)
	}
	// The import rebinds the Mac's replica to the new computer.
	if newIdentity := waitPaired(oldIdentity); newIdentity == oldIdentity {
		t.Fatal("the Mac replica is still bound to the old computer")
	}

	// Importing again adds nothing.
	json.Unmarshal([]byte(h.must("cloud", "import", archive)), &result)
	if result.RequestsAdded != 0 || result.RequestsExisting != 1 || result.SessionsRestored != 0 {
		t.Fatalf("second import %+v", result)
	}
}
