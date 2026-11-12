package ope2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/runta"
	"fennec/internal/terminalstream"
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

type daemonIdentity struct {
	PID     int    `json:"pid"`
	BuildID string `json:"build_id"`
}

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
	var diagnostics struct {
		Timings map[string]float64 `json:"timings_ms"`
	}
	if json.Unmarshal(raw, &diagnostics) == nil && len(diagnostics.Timings) > 0 {
		t.Logf("%s remote_timings_ms=%v", method, diagnostics.Timings)
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
	identity, _, err := c.call(ctx, "daemon.ping", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var original daemonIdentity
	if json.Unmarshal(identity, &original) != nil || original.PID <= 0 || original.BuildID == "" {
		t.Fatal("cannot identify installed daemon for performance measurement")
	}
	t.Cleanup(func() {
		verify, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		raw, _, err := c.call(verify, "daemon.ping", map[string]any{})
		var current daemonIdentity
		if err != nil || json.Unmarshal(raw, &current) != nil || current != original {
			t.Errorf("INVALID MEASUREMENT: daemon restarted or changed during live profiling (original pid=%d build=%s)", original.PID, original.BuildID)
		}
	})
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
	sshArgs := []string{"-T", "-S", "none", "-o", "ControlMaster=no", "-o", "IPQoS=ef", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + filepath.Join(cloud, "known_hosts"), "-i", filepath.Join(cloud, "runtime_key"), state.Runtime.ID + "@" + runta.ActiveSSHHost(home)}
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
	var resident struct {
		Ready      bool `json:"ready"`
		Generation int  `json:"generation"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _, err := c.call(ctx, "connection.status", nil)
		if err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(raw, &resident) != nil {
			t.Fatal("invalid connection status")
		}
		if resident.Ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resident channel failed to become ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
	initialGeneration := resident.Generation
	for _, method := range []string{"daemon.ping", "connection.status", "account.status", "agents.list", "settings.get", "takeover.get", "approvals.list", "state.get", "sessions.list", "sessions.refresh", "sessions.allocate"} {
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
		fixtureStarted := time.Now()
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
		// Keep the first-send gate above: a second send distinguishes startup
		// settling from steady-state input latency without hiding either cost.
		t.Run(fmt.Sprintf("send_existing_%d", sample), func(t *testing.T) {
			text := marker + "-existing"
			checked(t, c, "sessions.send", map[string]any{"session_id": id, "address": fixture.Address, "text": text})
			raw := checked(t, c, "sessions.read", params)
			if !bytes.Contains(raw, []byte(text)) {
				t.Fatal("existing-session send did not deliver literal input")
			}
		})
		t.Run(fmt.Sprintf("send_mature_%d", sample), func(t *testing.T) {
			// Herdr applies startup settling for ten seconds. Keep fresh-send
			// measurements above, and separately measure a mature terminal.
			if remaining := time.Until(fixtureStarted.Add(11 * time.Second)); remaining > 0 {
				timer := time.NewTimer(remaining)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			text := marker + "-mature"
			checked(t, c, "sessions.send", map[string]any{"session_id": id, "address": fixture.Address, "text": text})
			raw := checked(t, c, "sessions.read", params)
			if !bytes.Contains(raw, []byte(text)) {
				t.Fatal("mature-session send did not deliver literal input")
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
	raw, _, err = c.call(ctx, "connection.status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &resident) != nil || !resident.Ready || resident.Generation != initialGeneration {
		t.Fatal("hot operations replaced the resident channel")
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
	displayAnswered := false
	for {
		n, err := terminal.Read(buffer)
		if n > 0 {
			output.Write(buffer[:n])
			if !displayAnswered && bytes.Contains(output.Bytes(), []byte(terminalstream.DisplayQuery)) {
				terminal.Write((terminalstream.DisplayFacts{Foreground: "#cccccc", Background: "#181818", Cursor: "#cccccc", CellWidth: 8, CellHeight: 16}).Replies())
				displayAnswered = true
			}
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
