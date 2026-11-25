package byocimage

// TestEndToEnd runs Fennec's self-hosted flow with a daemon built from this
// source in an isolated HOME (its own control socket, settings and pairing; no
// Runta account; USER is changed so no Keychain credential is read), against a
// container of the image on local Docker:
//
//	pair -> preparation ready -> takeover -> launch -> TUI -> model reply ->
//	attach/detach -> daemon restart -> stop -> container restart -> unpair
//
//	docker build -f deploy/byoc/Dockerfile -t fennec-cloud:local .
//	FENNEC_BYOC_E2E=1 FENNEC_BYOC_IMAGE=fennec-cloud:local go test -v -run TestEndToEnd ./deploy/byoc
//
// Launching an agent needs its credentials. Without FENNEC_BYOC_E2E_CODEX_AUTH
// the test checks that the launch is refused as "not started". With it (a path
// to a codex auth.json, for example ~/.codex/auth.json), a copy is placed in
// the isolated HOME and synchronized into the test container, and the test
// launches codex and waits for a real model reply; the original file is only
// read, and the copies are removed with the HOME and the container's volume.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

type e2eHarness struct {
	t            *testing.T
	root         string // scratch files (daemon log)
	client       string // the fen CLI client
	daemonBinary string // the fen that runs control-daemon (the binary under test)
	home         string
	env          []string
	daemon       *exec.Cmd
	exited       chan struct{}
}

func (h *e2eHarness) fen(args ...string) (string, error) {
	return h.fenInput(nil, args...)
}

func (h *e2eHarness) fenInput(input []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, h.client, args...)
	command.Dir, command.Env = h.home, h.env
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err != nil {
		err = fmt.Errorf("fen %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), err
}

func (h *e2eHarness) must(args ...string) string {
	h.t.Helper()
	out, err := h.fen(args...)
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *e2eHarness) control(method string, params any, result any) error {
	data, _ := json.Marshal(params)
	if params == nil {
		data = []byte("{}")
	}
	out, err := h.fenInput(data, "control", method)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal([]byte(out), result)
}

type e2eState struct {
	Account struct {
		Status string `json:"status"`
	} `json:"account"`
	Runtime struct {
		Stage     string `json:"stage"`
		RuntimeID string `json:"runtime_id"`
		Error     string `json:"error"`
	} `json:"runtime"`
	Services struct {
		BrokerReady bool     `json:"broker_ready"`
		Active      []string `json:"active"`
	} `json:"services"`
}

func (h *e2eHarness) state() e2eState {
	h.t.Helper()
	var state e2eState
	if err := h.control("state.get", nil, &state); err != nil {
		h.t.Fatal(err)
	}
	return state
}

// waitReady waits for the paired computer to be prepared and its broker up.
func (h *e2eHarness) waitReady(within time.Duration) e2eState {
	h.t.Helper()
	deadline := time.Now().Add(within)
	for {
		state := h.state()
		if state.Account.Status == "paired" && state.Runtime.Stage == "ready" && state.Services.BrokerReady {
			return state
		}
		if state.Runtime.Stage == "failed" {
			h.t.Fatalf("preparation failed: %s\n%s", state.Runtime.Error, h.daemonLog())
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("not ready after %s: %+v\n%s", within, state, h.daemonLog())
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (h *e2eHarness) daemonLog() string {
	data, _ := os.ReadFile(filepath.Join(h.root, "daemon.log"))
	// Since THI-145 the daemon writes its log to dated files under HOME.
	days, _ := filepath.Glob(filepath.Join(h.home, ".config/fennec/logs/fennec-*.log"))
	for _, day := range days {
		more, _ := os.ReadFile(day)
		data = append(data, more...)
	}
	if len(data) > 4000 {
		data = data[len(data)-4000:]
	}
	return string(data)
}

func (h *e2eHarness) startDaemon() {
	h.t.Helper()
	log, err := os.OpenFile(filepath.Join(h.root, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		h.t.Fatal(err)
	}
	h.daemon = exec.Command(h.daemonBinary, "control-daemon")
	h.daemon.Dir, h.daemon.Env = h.home, h.env
	h.daemon.Stdout, h.daemon.Stderr = log, log
	if err := h.daemon.Start(); err != nil {
		h.t.Fatal(err)
	}
	h.exited = make(chan struct{})
	go func(command *exec.Cmd, exited chan struct{}) { command.Wait(); log.Close(); close(exited) }(h.daemon, h.exited)
	socket := filepath.Join(h.home, ".config/fennec/ipc/control.sock")
	for i := 0; ; i++ {
		if _, err := h.fen("control", "daemon.ping"); err == nil {
			return
		}
		select {
		case <-h.exited:
			h.t.Fatalf("daemon exited:\n%s", h.daemonLog())
		default:
		}
		if i > 200 {
			h.t.Fatalf("daemon did not listen on %s", socket)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (h *e2eHarness) stopDaemon() {
	if h.daemon == nil || h.daemon.Process == nil {
		return
	}
	_ = h.daemon.Process.Signal(syscall.SIGTERM)
	select {
	case <-h.exited:
	case <-time.After(15 * time.Second):
		_ = h.daemon.Process.Kill()
		<-h.exited
	}
	h.daemon = nil
}

func buildFennec(t *testing.T, root string) {
	t.Helper()
	source, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	builds := [][]string{{"bin/fen", ".", "", ""}}
	for _, arch := range []string{"amd64", "arm64"} {
		builds = append(builds, []string{"Resources/fennec-terminal-linux-" + arch, "./cmd/fennec-terminal", "linux", arch})
	}
	for _, b := range builds {
		command := exec.Command("go", "build", "-trimpath", "-o", filepath.Join(root, b[0]), b[1])
		command.Dir = source
		command.Env = os.Environ()
		if b[2] != "" {
			command.Env = append(command.Env, "CGO_ENABLED=0", "GOOS="+b[2], "GOARCH="+b[3])
		}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", b[0], err, output)
		}
	}
}

// e2eBinaries returns the fen CLI client, the fen that runs the daemon, and
// extra environment. By default both, and the terminal service, are built from
// this source. With FENNEC_BYOC_E2E_APP (a built Fennec.app) the daemon is the
// app's own fen, preparing the computer with the app's bundled
// Resources/fennec-terminal-linux-*. The app is copied to a temporary
// directory, where fen does not consider itself installed: the foreground
// daemon then starts no Trash watcher. The CLI client stays the source build:
// an installed-looking client would register the per-user launchd daemon job
// and replace the Fennec daemon already running on this Mac.
func e2eBinaries(t *testing.T, root string) (string, string, []string) {
	t.Helper()
	buildFennec(t, root)
	client := filepath.Join(root, "bin", "fen")
	app := os.Getenv("FENNEC_BYOC_E2E_APP")
	if app == "" {
		return client, client, nil
	}
	copyRoot, err := os.MkdirTemp("/tmp", "fe2e-app-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(copyRoot) })
	copied := filepath.Join(copyRoot, "Fennec.app")
	if output, err := exec.Command("ditto", app, copied).CombinedOutput(); err != nil {
		t.Fatalf("copy %s: %v\n%s", app, err, output)
	}
	if output, err := exec.Command("codesign", "--verify", "--strict", copied).CombinedOutput(); err != nil {
		t.Fatalf("the app under test is not validly signed: %v\n%s", err, output)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if _, err := os.Stat(filepath.Join(copied, "Contents/Resources/fennec-terminal-linux-"+arch)); err != nil {
			t.Fatalf("the app does not bundle the %s terminal service: %v", arch, err)
		}
	}
	t.Logf("testing the binaries bundled in %s", app)
	return client, filepath.Join(copied, "Contents/MacOS/fen"), []string{"TMPDIR=" + copyRoot}
}

func fileDigest(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unreadable: " + err.Error()
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// pollRead reads a session until its screen satisfies match.
func (h *e2eHarness) pollRead(id string, within time.Duration, match func(text, state string) bool) (string, string) {
	h.t.Helper()
	deadline := time.Now().Add(within)
	var text, state string
	for time.Now().Before(deadline) {
		out, err := h.fen("read", id)
		if err == nil {
			var read struct {
				Screen struct {
					Text string `json:"text"`
				} `json:"screen"`
				Terminal struct {
					State string `json:"state"`
				} `json:"terminal"`
			}
			if json.Unmarshal([]byte(out), &read) == nil {
				text, state = read.Screen.Text, read.Terminal.State
				if match(text, state) {
					return text, state
				}
			}
		}
		time.Sleep(time.Second)
	}
	h.t.Fatalf("session %s did not reach the expected screen within %s; state %q, screen:\n%s", id, within, state, text)
	return "", ""
}

func TestEndToEnd(t *testing.T) {
	image := os.Getenv("FENNEC_BYOC_IMAGE")
	if os.Getenv("FENNEC_BYOC_E2E") != "1" || image == "" {
		t.Skip("set FENNEC_BYOC_E2E=1 and FENNEC_BYOC_IMAGE to run the self-hosted end-to-end test")
	}
	codexAuth := os.Getenv("FENNEC_BYOC_E2E_CODEX_AUTH")
	root := t.TempDir()
	fen, daemonBinary, extraEnv := e2eBinaries(t, root)
	// Unix socket paths are limited to 104 bytes on macOS, and the HOME holds
	// control and attach sockets: keep it short.
	home, err := os.MkdirTemp("/tmp", "fe2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := os.MkdirAll(filepath.Join(home, ".config/fennec"), 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home, ".config/fennec/launch-profile.enabled"), nil, 0600)
	h := &e2eHarness{t: t, root: root, client: fen, daemonBinary: daemonBinary, home: home, env: append(extraEnv,
		"HOME="+home,
		"USER=fennec-e2e", // no Keychain entry belongs to this user
		"FENNEC_CREDENTIALS_FILE="+filepath.Join(home, "runta-credentials.json"),
		"PATH="+filepath.Dir(fen)+":/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin",
		"LANG=en_US.UTF-8",
	)}
	originalAuth := ""
	if codexAuth != "" {
		originalAuth = fileDigest(codexAuth)
		data, err := os.ReadFile(codexAuth)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".codex/auth.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if now := fileDigest(codexAuth); now != originalAuth {
				t.Errorf("the original codex credential changed during the test")
			}
		})
	}
	h.startDaemon()
	t.Cleanup(h.stopDaemon)
	h.must("settings", "magpie", "off")

	// Deploy: the device key is authorized at container start.
	var key struct {
		DeviceKey string `json:"device_key"`
	}
	if err := json.Unmarshal([]byte(h.must("cloud", "byoc", "key")), &key); err != nil || !strings.HasPrefix(key.DeviceKey, "ssh-ed25519 ") {
		t.Fatalf("device key: %v", err)
	}
	volume := randomName("fennec-byoc-e2e-")
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() })
	// A fixed host port, as in a real deployment: Docker reassigns a picked one
	// when the container restarts, and the pairing pins the port.
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()
	c, endpoint := startOn(t, image, volume, key.DeviceKey, port)
	fingerprint := docker(t, "exec", c.name, "fennec-cloud", "fingerprint")
	machine := docker(t, "exec", c.name, "fennec-cloud", "identity")

	// Pair, then preparation runs by itself.
	paired := time.Now()
	h.must("cloud", "byoc", "pair", net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)), "--fingerprint", fingerprint)
	state := h.waitReady(10 * time.Minute)
	t.Logf("pair -> ready in %s", time.Since(paired).Round(100*time.Millisecond))
	if state.Runtime.RuntimeID != machine {
		t.Fatalf("prepared %q, the container is %q", state.Runtime.RuntimeID, machine)
	}
	// The terminal service on the computer is the one shipped next to the
	// daemon under test (content-addressed by its SHA-256).
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[docker(t, "exec", c.name, "uname", "-m")]
	shipped := fileDigest(filepath.Join(filepath.Dir(h.daemonBinary), "..", "Resources", "fennec-terminal-linux-"+arch))
	installed := docker(t, "exec", "-u", "fennec", c.name, "sh", "-c", `basename "$(dirname "$(readlink "$HOME/.local/fennec/bin/fennec-terminal")")"`)
	if installed != shipped {
		t.Fatalf("the computer runs terminal service %s, the daemon ships %s", installed, shipped)
	}
	t.Logf("terminal service on the computer is the shipped %s artifact %s", arch, shipped[:12])
	h.must("takeover", "codex", "on")

	t.Run("sessions stay available while the computer is prepared again", func(t *testing.T) {
		// Hold preparation open deterministically with the product's own lock:
		// with codex missing, re-preparation falls back to full preparation,
		// whose installer waits on install-locks/codex.lock (flock -w 180)
		// until this test releases it.
		h.waitReady(5 * time.Minute)
		lock := `"$HOME/.local/fennec/install-locks/codex.lock"`
		inContainer := func(script string) error {
			return exec.Command("docker", "exec", "-u", "fennec", c.name, "sh", "-c", script).Run()
		}
		if err := inContainer(`rm -f "$HOME/.local/fennec/agents/bin/codex" "$HOME/.local/bin/codex"`); err != nil {
			t.Fatal(err)
		}
		// -o: only this flock process holds the lock, so killing it releases it.
		if err := exec.Command("docker", "exec", "-d", "-u", "fennec", c.name, "sh", "-c", "exec flock -o "+lock+" sleep 600").Run(); err != nil {
			t.Fatal(err)
		}
		released := false
		release := func() {
			if !released {
				released = true
				_ = exec.Command("docker", "exec", c.name, "pkill", "-f", "codex.lock sleep 600").Run()
			}
		}
		defer release()
		for i := 0; inContainer("flock -n "+lock+" true") == nil; i++ {
			if i > 50 {
				t.Fatal("the test could not take the installation lock")
			}
			time.Sleep(100 * time.Millisecond)
		}

		if err := h.control("runtime.retry", nil, nil); err != nil && !strings.Contains(err.Error(), "already running") {
			t.Fatal(err)
		}
		var runtime struct {
			Stage     string `json:"stage"`
			RuntimeID string `json:"runtime_id"`
		}
		held := time.Now()
		// Stay inside the window for several seconds: preparation cannot end
		// while the lock is held, so every check below runs during it.
		for time.Since(held) < 5*time.Second {
			if err := h.control("runtime.status", nil, &runtime); err != nil {
				t.Fatal(err)
			}
			if runtime.Stage != "preparing" || runtime.RuntimeID != machine {
				t.Fatalf("expected the same computer in preparation, got %+v", runtime)
			}
			if err := h.control("sessions.list", nil, nil); err != nil {
				t.Fatalf("sessions.list refused during re-preparation: %v", err)
			}
			if err := h.control("sessions.refresh", nil, nil); err != nil {
				t.Fatalf("sessions.refresh refused during re-preparation: %v", err)
			}
			time.Sleep(500 * time.Millisecond)
		}
		if codexAuth == "" {
			// The broker answers during preparation; without credentials it
			// refuses before starting anything.
			if _, err := h.fen("launch", "codex"); err == nil || !strings.Contains(err.Error(), "was not started") || strings.Contains(err.Error(), "not ready yet") {
				t.Fatalf("launch during re-preparation did not reach the broker: %v", err)
			}
		}
		t.Logf("held preparation open for %s; stage %q, computer kept", time.Since(held).Round(100*time.Millisecond), runtime.Stage)

		release()
		h.waitReady(5 * time.Minute)
		if err := inContainer(`test -x "$HOME/.local/fennec/agents/bin/codex" && test -e "$HOME/.local/bin/codex"`); err != nil {
			t.Fatal("preparation did not reinstall codex after the lock was released")
		}
	})

	if codexAuth == "" {
		t.Run("a launch without credentials is refused as not started", func(t *testing.T) {
			_, err := h.fen("launch", "codex")
			if err == nil || !strings.Contains(err.Error(), "was not started") || strings.Contains(err.Error(), "not confirmed") {
				t.Fatalf("unexpected launch result: %v", err)
			}
		})
	} else {
		var session string
		t.Run("launch, TUI and a model reply", func(t *testing.T) {
			if !contains(h.state().Services.Active, "codex") {
				t.Fatalf("codex is not ready with the supplied credential:\n%s", h.daemonLog())
			}
			started := time.Now()
			var receipt struct {
				SessionID string `json:"session_id"`
				RuntimeID string `json:"runtime_id"`
			}
			if err := json.Unmarshal([]byte(h.must("launch", "codex")), &receipt); err != nil || receipt.SessionID == "" || receipt.RuntimeID != machine {
				t.Fatalf("launch receipt: %+v %v", receipt, err)
			}
			session = receipt.SessionID
			t.Logf("launch receipt in %s", time.Since(started).Round(time.Millisecond))
			h.pollRead(session, time.Minute, func(text, _ string) bool { return strings.Contains(text, "OpenAI Codex") })
			t.Logf("input-ready TUI after %s", time.Since(started).Round(100*time.Millisecond))
			h.must("send", session, "Reply with exactly the word BYOC-OK and nothing else. Do not run any commands.")
			_, _ = h.fen("send", session, "\r")
			h.pollRead(session, 3*time.Minute, func(text, _ string) bool { return strings.Contains(text, "• BYOC-OK") })
		})
		if session == "" {
			t.FailNow()
		}

		t.Run("attach and detach over a terminal", func(t *testing.T) {
			command := exec.Command(h.client, "attach", session)
			command.Dir, command.Env = home, append(h.env, "TERM=xterm-256color")
			terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 40, Cols: 120})
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			var mu sync.Mutex
			var screen bytes.Buffer
			go func() {
				buffer := make([]byte, 4096)
				for {
					n, err := terminal.Read(buffer)
					mu.Lock()
					screen.Write(buffer[:n])
					mu.Unlock()
					if err != nil {
						return
					}
				}
			}()
			waitFor := func(needle string) {
				t.Helper()
				deadline := time.Now().Add(30 * time.Second)
				for time.Now().Before(deadline) {
					mu.Lock()
					found := strings.Contains(screen.String(), needle)
					mu.Unlock()
					if found {
						return
					}
					time.Sleep(100 * time.Millisecond)
				}
				mu.Lock()
				defer mu.Unlock()
				t.Fatalf("terminal never showed %q:\n%q", needle, tail(screen.String(), 2000))
			}
			waitFor("BYOC-OK")
			time.Sleep(500 * time.Millisecond)
			if _, err := terminal.Write([]byte{3}); err != nil {
				t.Fatal(err)
			}
			waitFor("fen attach " + session)
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("detach exited with %v", err)
				}
			case <-time.After(15 * time.Second):
				_ = command.Process.Kill()
				t.Fatal("attach did not exit after Ctrl+C")
			}
			h.pollRead(session, 10*time.Second, func(text, state string) bool { return state != "finished" && strings.Contains(text, "BYOC-OK") })
		})

		t.Run("the paired computer and its session survive a daemon restart", func(t *testing.T) {
			h.stopDaemon()
			h.startDaemon()
			h.waitReady(2 * time.Minute)
			h.pollRead(session, 30*time.Second, func(text, state string) bool { return state != "finished" && strings.Contains(text, "BYOC-OK") })
		})

		t.Run("stop ends the cloud process", func(t *testing.T) {
			h.must("stop", session)
			h.pollRead(session, 30*time.Second, func(_, state string) bool { return state == "finished" })
			out, _ := exec.Command("docker", "exec", c.name, "ps", "-eo", "comm=").Output()
			for _, name := range strings.Fields(string(out)) {
				if strings.HasPrefix(name, "codex") {
					t.Fatalf("codex still runs in the container: %s", out)
				}
			}
		})
	}

	t.Run("reconnects after the container restarts", func(t *testing.T) {
		docker(t, "restart", "-t", "2", c.name)
		// The broker notices the lost connection, then preparation recovers it.
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			if err := h.control("sessions.refresh", nil, nil); err == nil {
				break
			}
			time.Sleep(time.Second)
		}
		h.waitReady(3 * time.Minute)
		if err := h.control("sessions.refresh", nil, nil); err != nil {
			t.Fatalf("sessions unavailable after the container restarted: %v", err)
		}
	})

	t.Run("a stopped computer is reported unreachable until it returns", func(t *testing.T) {
		docker(t, "stop", "-t", "2", c.name)
		stopped := time.Now()
		deadline := stopped.Add(2 * time.Minute)
		for h.state().Runtime.Stage != "unreachable" {
			if time.Now().After(deadline) {
				t.Fatalf("still %q two minutes after the computer stopped", h.state().Runtime.Stage)
			}
			time.Sleep(time.Second)
		}
		t.Logf("reported unreachable %s after the computer stopped", time.Since(stopped).Round(time.Second))
		docker(t, "start", c.name)
		returned := time.Now()
		h.waitReady(2 * time.Minute)
		t.Logf("ready again %s after the computer returned", time.Since(returned).Round(time.Second))
	})

	t.Run("unpair forgets the computer", func(t *testing.T) {
		h.must("cloud", "byoc", "unpair", machine)
		// Unpairing restores the stored Runta account in the background; this
		// HOME has none, so it settles signed out.
		status := ""
		for i := 0; i < 50; i++ {
			if status = h.state().Account.Status; status == "signed_out" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if status != "signed_out" {
			t.Fatalf("account after unpairing: %q", status)
		}
		if _, err := os.Stat(filepath.Join(home, ".config/fennec/byoc")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pairing material left after unpairing: %v", err)
		}
	})
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func tail(text string, n int) string {
	if len(text) > n {
		return text[len(text)-n:]
	}
	return text
}
