package ope2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

const warmLimit = 500 * time.Millisecond

type liveClient struct{ binary string }

func (c liveClient) call(ctx context.Context, method string, params any) (json.RawMessage, time.Duration, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, 0, err
	}
	command := exec.CommandContext(ctx, c.binary, "fen", "control", method)
	command.Stdin = bytes.NewReader(data)
	started := time.Now()
	raw, err := command.Output()
	elapsed := time.Since(started)
	if exit, ok := err.(*exec.ExitError); ok {
		err = fmt.Errorf("%s: %s", method, strings.TrimSpace(string(exit.Stderr)))
	}
	return raw, elapsed, err
}
func checked(t *testing.T, c liveClient, method string, params any) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	raw, elapsed, err := c.call(ctx, method, params)
	t.Logf("%s wall_ms=%.3f limit_ms=500", method, float64(elapsed.Microseconds())/1000)
	if err != nil {
		t.Fatalf("operation failed: %v", err)
	}
	if elapsed > warmLimit {
		t.Errorf("PERFORMANCE REGRESSION: %s took %s, exceeds 500ms", method, elapsed)
	}
	if !json.Valid(raw) {
		t.Fatal("invalid operation response")
	}
	return raw
}
func nonce(t *testing.T) string {
	t.Helper()
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(data[:])
}

// This is an explicit live acceptance test, never an implicit part of unit tests.
// make test-e2e-ops enables it; missing prerequisites then fail, rather than skip.
func TestSessionOperations500ms(t *testing.T) {
	if os.Getenv("FENNEC_LIVE_E2E") != "1" {
		t.Skip("explicit live test: make test-e2e-ops")
	}
	binary := os.Getenv("FENNEC_E2E_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("FENNEC_E2E_BINARY must be an absolute installed binary path")
	}
	c := liveClient{binary}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	raw, _, err := c.call(ctx, "state.get", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Account struct {
			Status string `json:"status"`
		} `json:"account"`
		Runtime struct {
			ID string `json:"runtime_id"`
		} `json:"runtime"`
		Services struct {
			Ready bool `json:"broker_ready"`
		} `json:"services"`
	}
	if json.Unmarshal(raw, &state) != nil || state.Account.Status != "authorized" || !state.Services.Ready || state.Runtime.ID == "" {
		t.Fatal("installed daemon must be signed in and ready; run fen start first")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cloud := filepath.Join(home, ".config/fennec/cloud")
	sshArgs := []string{"-T", "-S", "none", "-o", "ControlMaster=no", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + filepath.Join(cloud, "known_hosts"), "-i", filepath.Join(cloud, "runtime_key"), state.Runtime.ID + "@ssh.runta.com"}
	ssh := func(script string) *exec.Cmd {
		return exec.CommandContext(ctx, "/usr/bin/ssh", append(append([]string{}, sshArgs...), script)...)
	}
	var initialPID float64
	raw, _, err = c.call(ctx, "daemon.ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ping map[string]any
	json.Unmarshal(raw, &ping)
	initialPID, _ = ping["pid"].(float64)
	t.Run("cold_ssh", func(t *testing.T) {
		start := time.Now()
		if err := ssh("true").Run(); err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		t.Logf("cold SSH wall_ms=%.3f limit_ms=5000", float64(elapsed.Microseconds())/1000)
		if elapsed > 5*time.Second {
			t.Errorf("cold SSH exceeds 5s: %s", elapsed)
		}
	})
	for _, method := range []string{"daemon.ping", "account.status", "agents.list", "settings.get", "takeover.get", "approvals.list", "state.get", "sessions.list", "sessions.refresh", "sessions.allocate"} {
		t.Run(method, func(t *testing.T) {
			for i := 0; i < 3; i++ {
				checked(t, c, method, map[string]any{})
			}
		})
	}
	// The fixture is a bounded cat process, not an existing user agent. Its setup
	// handshake is excluded from hot timings and recorded by cold_ssh above.
	for sample := 0; sample < 3; sample++ {
		id := nonce(t)
		create := ssh("/root/.local/bin/herdr-core run --json --bg --no-parent --no-notify --label fennec.session=" + id + " --label fennec.agent=codex -- /usr/bin/timeout 120 /bin/cat")
		raw, err := create.Output()
		if err != nil {
			t.Fatal("fixture creation", err)
		}
		var fixture struct {
			Address string `json:"address"`
		}
		if json.Unmarshal(raw, &fixture) != nil || !regexp.MustCompile(`^t[0-9a-z]+$`).MatchString(fixture.Address) {
			t.Fatal("invalid fixture")
		}
		params := map[string]any{"session_id": id, "address": fixture.Address}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			raw, _, err := c.call(cleanup, "sessions.read", params)
			if err != nil {
				return
			}
			var read struct {
				Terminal struct {
					State string `json:"state"`
				} `json:"terminal"`
			}
			if json.Unmarshal(raw, &read) == nil && read.Terminal.State == "live" {
				_, _, _ = c.call(cleanup, "sessions.stop", params)
			}
		})
		marker := "fennec-e2e-" + id[:8]
		t.Run(fmt.Sprintf("send_%d", sample), func(t *testing.T) {
			checked(t, c, "sessions.send", map[string]any{"session_id": id, "address": fixture.Address, "text": marker})
		})
		t.Run(fmt.Sprintf("read_%d", sample), func(t *testing.T) {
			raw := checked(t, c, "sessions.read", params)
			if !bytes.Contains(raw, []byte(marker)) {
				t.Fatal("send did not deliver literal input")
			}
		})
		// Refresh must complete remotely before testing attachment; a cached list is
		// not accepted as evidence of a remote operation's wall time.
		if _, _, err := c.call(ctx, "sessions.refresh", nil); err != nil {
			t.Fatal(err)
		}
		for _, operation := range []string{"attach", "resume"} {
			t.Run(fmt.Sprintf("%s_usable_%d", operation, sample), func(t *testing.T) {
				elapsed, err := attachReady(ctx, binary, operation, id, marker)
				t.Logf("%s usable_ms=%.3f", operation, float64(elapsed.Microseconds())/1000)
				if err != nil {
					t.Fatal(err)
				}
				if elapsed > warmLimit {
					t.Errorf("PERFORMANCE REGRESSION: usable %s took %s, exceeds 500ms", operation, elapsed)
				}
			})
		}
		t.Run(fmt.Sprintf("stop_%d", sample), func(t *testing.T) {
			raw := checked(t, c, "sessions.stop", params)
			var reply map[string]any
			json.Unmarshal(raw, &reply)
			if reply["state"] != "finished" {
				t.Fatal("stop not confirmed")
			}
			cached, _, err := c.call(ctx, "sessions.list", nil)
			if err != nil {
				t.Fatal(err)
			}
			var listing struct {
				Terminals []struct {
					State  string            `json:"state"`
					Labels map[string]string `json:"labels"`
				} `json:"terminals"`
			}
			json.Unmarshal(cached, &listing)
			for _, row := range listing.Terminals {
				if row.State == "live" && row.Labels["fennec.session"] == id {
					t.Fatal("stale cache resurrected stopped session")
				}
			}
		})
	}
	t.Run("launch_receipt", func(t *testing.T) {
		raw, _, err := c.call(ctx, "sessions.allocate", nil)
		if err != nil {
			t.Fatal(err)
		}
		var allocation map[string]string
		if json.Unmarshal(raw, &allocation) != nil {
			t.Fatal("allocation")
		}
		receipt := checked(t, c, "sessions.launch", map[string]any{"session_id": allocation["session_id"], "agent": "codex", "directory": t.TempDir(), "arguments": []string{"--version"}})
		var result map[string]any
		if json.Unmarshal(receipt, &result) != nil || result["session_id"] != allocation["session_id"] {
			t.Fatal("launch receipt mismatch")
		}
	})
	t.Run("launch_usable_tui", func(t *testing.T) {
		_, source, _, _ := runtime.Caller(0)
		root := filepath.Dir(filepath.Dir(filepath.Dir(source)))
		command := exec.CommandContext(ctx, "go", "run", "./cmd/profile-launch", "--binary", binary, "--mode", "warm", "--samples", "3")
		command.Dir = root
		output, err := command.Output()
		if err != nil {
			t.Fatalf("real TUI launch failed: %v", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(output))
		for sample := 0; sample < 3; sample++ {
			var result struct {
				ScreenMS float64 `json:"screen_ms"`
				Exit     int     `json:"exit_code"`
			}
			if err := decoder.Decode(&result); err != nil {
				t.Fatal(err)
			}
			t.Logf("launch usable_ms=%.3f", result.ScreenMS)
			if result.ScreenMS <= 0 || result.ScreenMS > 500 || result.Exit != 0 {
				t.Errorf("PERFORMANCE REGRESSION: usable Codex TUI %.3fms (limit 500ms), exit=%d", result.ScreenMS, result.Exit)
			}
		}
	})
	raw, _, err = c.call(ctx, "daemon.ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &ping)
	if ping["pid"] != initialPID {
		t.Fatal("daemon restarted during hot-operation tests")
	}
}

func attachReady(parent context.Context, binary, operation, id, marker string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "fen", operation, id)
	command.Env = append(os.Environ(), "TERM=xterm-256color")
	started := time.Now()
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return 0, err
	}
	defer terminal.Close()
	var output bytes.Buffer
	buffer := make([]byte, 16384)
	elapsed := time.Duration(0)
	for {
		n, err := terminal.Read(buffer)
		if n > 0 {
			output.Write(buffer[:n])
			if elapsed == 0 && bytes.Contains(output.Bytes(), []byte(marker)) {
				elapsed = time.Since(started)
				terminal.Write([]byte{28})
			}
		}
		if err != nil {
			break
		}
	}
	if err := command.Wait(); err != nil {
		return elapsed, fmt.Errorf("attach failed: %w", err)
	}
	if elapsed == 0 {
		return 0, fmt.Errorf("usable terminal was not observed")
	}
	return elapsed, nil
}
