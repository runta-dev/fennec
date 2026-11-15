// verify-reconnect exercises an explicitly requested daemon restart, Wi-Fi
// cycle, or staged update with an owned Codex terminal and no model prompt.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/agent"
	"fennec/internal/connectivity"
	"fennec/internal/control"
	"fennec/internal/launcher"
	"fennec/internal/terminalstate"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

type identity struct {
	PID   int    `json:"pid"`
	Build string `json:"build_id"`
}

type terminalIdentity struct {
	Address string `json:"address"`
	Created int64  `json:"created_ms"`
}

func listedIdentity(raw json.RawMessage, address string) (terminalIdentity, error) {
	var list struct {
		Terminals []terminalIdentity `json:"terminals"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return terminalIdentity{}, err
	}
	for _, item := range list.Terminals {
		if item.Address == address && item.Created > 0 {
			return item, nil
		}
	}
	return terminalIdentity{}, fmt.Errorf("owned terminal missing from authoritative listing")
}

type capture struct {
	sync.Mutex
	text   strings.Builder
	chunks []captureChunk
}

type captureChunk struct {
	at     time.Time
	offset int
}

func (c *capture) since(at time.Time) string {
	c.Lock()
	defer c.Unlock()
	for _, chunk := range c.chunks {
		if !chunk.at.Before(at) {
			return c.text.String()[chunk.offset:]
		}
	}
	return ""
}

func (c *capture) read() string { c.Lock(); defer c.Unlock(); return c.text.String() }

var ansi = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (result error) {
	binary := flag.String("binary", "", "candidate CLI, without replacing the installed app")
	installed := flag.String("installed", "/Applications/Fennec.app/Contents/MacOS/fen", "installed release CLI")
	restart := flag.Bool("restart-daemon", false, "explicitly restart the actual installed daemon")
	wifi := flag.String("cycle-wifi", "", "explicitly cycle this Mac Wi-Fi interface for three seconds")
	update := flag.Bool("install-update", false, "explicitly install the already verified local test update through update.install")
	budget := flag.Duration("max-recovery", 0, "fail if recovery after network restoration exceeds this duration")
	shim := flag.Bool("shim", false, "launch codex through a production PATH shim instead of attaching")
	installedShim := flag.Bool("installed-shim", false, "launch through the existing installed codex shim")
	logPath := flag.String("terminal-log", "/tmp/fennec-reconnect-live/terminal.log", "owned test terminal transcript")
	flag.Parse()
	if *installedShim {
		*shim = true
		*binary = *installed
	}
	modes := 0
	for _, active := range []bool{*restart, *wifi != "", *update} {
		if active {
			modes++
		}
	}
	if modes != 1 || !filepath.IsAbs(*binary) {
		return fmt.Errorf("absolute -binary and exactly one explicit disruption mode required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	profileMarker := filepath.Join(home, ".config/fennec/launch-profile.enabled")
	if file, err := os.OpenFile(profileMarker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
		file.Close()
		defer os.Remove(profileMarker)
	} else if !os.IsExist(err) {
		return err
	}
	client := control.Client{Socket: filepath.Join(home, ".config/fennec/ipc/control.sock")}
	call := func(method string, params, out any) error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return client.Call(ctx, method, params, out)
	}
	var before identity
	if err := call("daemon.ping", nil, &before); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(nonce[:])
	directory, err := os.MkdirTemp("", "fennec-reconnect-project-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	var command *exec.Cmd
	var terminal *os.File
	// Start exactly once, with a known identity even if the reply is lost.
	var receipt launcher.LaunchReceipt
	if *shim {
		command, terminal, receipt, err = launchViaShim(ctx, home, directory, *binary, *installedShim)
		if err != nil {
			return err
		}
		id = receipt.SessionID
		defer terminal.Close()
		defer command.Process.Kill()
	} else if err := call("sessions.launch", map[string]any{"session_id": id, "agent": "codex", "directory": directory, "arguments": []string{"--no-alt-screen"}}, &receipt); err != nil {
		return fmt.Errorf("launch %s uncertain; inspect before retrying: %w", id, err)
	}
	if receipt.SessionID != id || receipt.Address == "" {
		return fmt.Errorf("invalid owned session receipt for %s", id)
	}
	params := map[string]any{"session_id": id, "address": receipt.Address, "expected_runtime_id": receipt.RuntimeID}
	defer func() {
		var stopped struct {
			State string `json:"state"`
		}
		if err := call("sessions.stop", params, &stopped); err != nil || stopped.State != "finished" {
			result = fmt.Errorf("%v; owned session %s cleanup unconfirmed: %v", result, id, err)
		} else {
			fmt.Println("Owned test session cleanup confirmed")
		}
	}()
	var refreshed json.RawMessage
	if err := call("sessions.refresh", nil, &refreshed); err != nil {
		return err
	}
	initial, err := listedIdentity(refreshed, receipt.Address)
	if err != nil {
		return err
	}
	if command == nil {
		command = exec.CommandContext(ctx, *binary, "fen", "attach", id)
		command.Env = append(os.Environ(), "TERM=xterm-256color", "NO_COLOR=", "FENNEC_DEBUG=")
		terminal, err = pty.StartWithSize(command, &pty.Winsize{Rows: 40, Cols: 120})
		if err != nil {
			return err
		}
		defer terminal.Close()
		defer command.Process.Kill()
	}
	var output capture
	done := make(chan struct{})
	responses := map[string]string{"\x1b]10;?": "\x1b]10;rgb:cccc/cccc/cccc\x1b\\", "\x1b]11;?": "\x1b]11;rgb:1818/1818/1818\x1b\\", "\x1b]12;?": "\x1b]12;rgb:cccc/cccc/cccc\x1b\\", "\x1b[16t": "\x1b[6;16;8t", "\x1b[c": "\x1b[?1;2c", "\x1b[6n": "\x1b[1;1R"}
	go func() {
		defer close(done)
		buffer := make([]byte, 32768)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				text := string(buffer[:n])
				output.Lock()
				output.chunks = append(output.chunks, captureChunk{time.Now(), output.text.Len()})
				output.text.WriteString(text)
				output.Unlock()
				for query, reply := range responses {
					if strings.Contains(text, query) {
						terminal.Write([]byte(reply))
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { _ = os.WriteFile(*logPath, []byte(output.read()), 0600) }()
	wait := func(label string, timeout time.Duration, check func() bool) error {
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			if check() {
				return nil
			}
			select {
			case <-done:
				if check() {
					return nil
				}
				return fmt.Errorf("terminal exited while waiting for %s", label)
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline.C:
				return fmt.Errorf("timeout waiting for %s; transcript: %s", label, *logPath)
			case <-ticker.C:
			}
		}
	}
	if err := wait("Codex composer", 30*time.Second, func() bool {
		plain := ansi.ReplaceAllString(output.read(), "")
		return strings.Contains(plain, "›") && (strings.Contains(plain, "Ask Codex") || strings.Contains(plain, "OpenAI Codex"))
	}); err != nil {
		return err
	}
	shortID := id[:min(8, len(id))]
	beforeMarker := "reconnectbefore" + shortID
	terminal.Write([]byte(beforeMarker))
	if err := wait("initial input echo", 5*time.Second, func() bool { return strings.Contains(ansi.ReplaceAllString(output.read(), ""), beforeMarker) }); err != nil {
		return err
	}
	receiptPath := filepath.Join(home, ".config/fennec/launches", id+".json")
	oldReceipt, err := os.Stat(receiptPath)
	if err != nil {
		return err
	}
	offset := len(output.read())
	started := time.Now()
	available := started
	mode := "daemon"
	var stopElapsed time.Duration
	fmt.Printf("Disrupting installed connection pid=%d with owned session=%s address=%s client_pid=%d\n", before.PID, id, receipt.Address, command.Process.Pid)
	// Run this verifier through cmd/run-macos-check so launchd owns it separately
	// from the daemon under test. nohup alone does not survive job cleanup.
	// Always request restart even when stop returns an error.
	if *restart {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		stopErr := exec.CommandContext(stopCtx, *installed, "daemon", "stop").Run()
		stopCancel()
		stopElapsed = time.Since(started)
		startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
		startErr := exec.CommandContext(startCtx, *installed, "start").Run()
		startCancel()
		if startErr != nil {
			return fmt.Errorf("installed daemon restart failed: %w (stop: %v)", startErr, stopErr)
		}
	} else if *wifi != "" {
		mode = "wifi"
		restore := func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			return exec.CommandContext(ctx, "/usr/sbin/networksetup", "-setairportpower", *wifi, "on").Run()
		}
		defer restore()
		if err := exec.CommandContext(ctx, "/usr/sbin/networksetup", "-setairportpower", *wifi, "off").Run(); err != nil {
			return err
		}
		time.Sleep(3 * time.Second)
		if err := restore(); err != nil {
			return err
		}
		if err := wait("local routing after Wi-Fi restore", 20*time.Second, connectivity.ProbeRoutingTable); err != nil {
			return err
		}
		available = time.Now()
		fmt.Printf("Local network restored after %dms\n", available.Sub(started).Milliseconds())
	} else {
		mode = "update"
		var staged struct {
			State string `json:"state"`
		}
		if err := call("update.status", nil, &staged); err != nil || staged.State != "ready" {
			return fmt.Errorf("verified test update is not ready: %v", err)
		}
		// The old daemon can disappear before its response. Never replay an
		// uncertain install; verify the new process and signed build identity.
		var reply json.RawMessage
		_ = call("update.install", nil, &reply)
	}
	var after identity
	if err := wait("installed daemon identity", 45*time.Second, func() bool {
		probe, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		if client.Call(probe, "daemon.ping", nil, &after) != nil {
			return false
		}
		if mode == "wifi" {
			return after.PID == before.PID && after.Build == before.Build
		}
		return after.PID != before.PID
	}); err != nil {
		return err
	}
	daemonElapsed := time.Since(started)
	if mode == "daemon" && after.Build != before.Build {
		return fmt.Errorf("unexpected build change during daemon restart")
	}
	if mode == "update" {
		build, err := os.ReadFile(filepath.Join(filepath.Dir(*installed), "../Resources/control-build-id"))
		if err != nil || after.Build != strings.TrimSpace(string(build)) || after.Build == before.Build {
			return fmt.Errorf("updated daemon build identity not confirmed")
		}
	}
	var restoredAt time.Time
	if err := wait("reattachment receipt", 80*time.Second, func() bool {
		info, err := os.Stat(receiptPath)
		if err == nil && info.ModTime().After(oldReceipt.ModTime()) {
			restoredAt = info.ModTime()
			return true
		}
		return false
	}); err != nil {
		return err
	}
	// A receipt alone is insufficient: send through the surviving CLI and require
	// both rendered input and an authoritative cloud terminal readback.
	afterMarker := "reconnectafter" + shortID
	if err := wait("restored composer", 15*time.Second, func() bool {
		text := output.since(restoredAt)
		return strings.Contains(ansi.ReplaceAllString(text, ""), beforeMarker)
	}); err != nil {
		return err
	}
	terminal.Write([]byte(afterMarker))
	if err := wait("restored input echo", 8*time.Second, func() bool { return strings.Contains(ansi.ReplaceAllString(output.read()[offset:], ""), afterMarker) }); err != nil {
		return err
	}
	recovered := time.Since(started)
	recoveryAfterNetwork := time.Since(available)
	var readback json.RawMessage
	if err := call("sessions.read", params, &readback); err != nil {
		return err
	}
	if !strings.Contains(string(readback), afterMarker) {
		return fmt.Errorf("restored input missing from cloud terminal readback")
	}
	if err := call("sessions.refresh", nil, &refreshed); err != nil {
		return err
	}
	current, err := listedIdentity(refreshed, receipt.Address)
	if err != nil || current != initial {
		return fmt.Errorf("cloud terminal identity changed or is unverified")
	}
	currentReceipt, err := launcher.ReadReceipt(filepath.Dir(receiptPath), id)
	if err != nil || currentReceipt != receipt {
		return fmt.Errorf("attachment receipt changed: %v", err)
	}
	segment := output.read()[offset:]
	if strings.Contains(segment, "Retry #") {
		return fmt.Errorf("reconnect UI displayed a retry counter")
	}
	if strings.Contains(segment, terminalstate.Reset) || strings.Contains(segment, "Session detached") {
		return fmt.Errorf("CLI restored the terminal during recovery")
	}
	if err := command.Process.Signal(syscall.Signal(0)); err != nil {
		return fmt.Errorf("original CLI exited: %w", err)
	}
	fmt.Printf("Recovery verified in %dms: same CLI, same cloud terminal, restored input read back\n", recovered.Milliseconds())
	cancelled := time.Now()
	terminal.Write([]byte{3})
	if err := wait("detach notice", 2*time.Second, func() bool { return strings.Contains(output.read()[offset:], "fen attach "+id) }); err != nil {
		return err
	}
	detachLatency := time.Since(cancelled)
	if err := command.Wait(); err != nil {
		return err
	}
	text := output.read()[offset:]
	if strings.Index(text, terminalstate.Reset) < 0 || strings.Index(text, terminalstate.Reset) > strings.Index(text, "fen attach "+id) {
		return fmt.Errorf("detach notice preceded terminal restoration")
	}
	if detachLatency > 500*time.Millisecond {
		return fmt.Errorf("detach exceeded 500ms: %s", detachLatency)
	}
	status := "PASS"
	var budgetErr error
	if *budget > 0 && recoveryAfterNetwork > *budget {
		status = "FAIL"
		budgetErr = fmt.Errorf("%s recovery %s exceeded %s", mode, recoveryAfterNetwork, *budget)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"mode": mode, "recovery_after_network_ms": recoveryAfterNetwork.Milliseconds(),
		"stop_ms": stopElapsed.Milliseconds(), "daemon_ready_ms": daemonElapsed.Milliseconds(),
		"production_shim": *shim,
		"installed_shim":  *installedShim,
		"result":          status, "daemon_before": before, "daemon_after": after,
		"session_id": id, "address": receipt.Address, "created_ms": current.Created,
		"client_pid": command.Process.Pid, "recovery_to_input_ms": recovered.Milliseconds(),
		"loading_visible":   strings.Contains(segment, "Reconnecting") || strings.Contains(segment, "Retrying in"),
		"countdown_visible": strings.Contains(segment, "Retrying in"), "detach_ms": detachLatency.Milliseconds(),
		"retry_counter_visible": strings.Contains(segment, "Retry #"),
		"same_session":          true, "input_verified_in_cloud": true, "terminal_log": *logPath,
	}); err != nil {
		return err
	}
	return budgetErr
}

// Use the shipped wrapper generator and real dispatch-agent/run-agent code.
// Keep the wrapper in a private PATH directory so the user's installed release
// remains intact. The session ID is correlated with this exact exec'ed CLI PID.
func launchViaShim(ctx context.Context, home, directory, binary string, installed bool) (*exec.Cmd, *os.File, launcher.LaunchReceipt, error) {
	var receipt launcher.LaunchReceipt
	shims := filepath.Join(directory, "shims")
	paths := agent.ExecutableSearchPath(home)
	if installed {
		shims = filepath.Join(home, ".local/share/fennec/shims")
		data, err := os.ReadFile(filepath.Join(shims, "codex"))
		if err != nil || !bytes.Contains(data, []byte(binary)) {
			return nil, nil, receipt, fmt.Errorf("installed codex shim does not target %s: %v", binary, err)
		}
	} else if err := launcher.InstallShims(shims, binary, paths); err != nil {
		return nil, nil, receipt, err
	}
	root := filepath.Join(home, ".config/fennec")
	markerPath := filepath.Join(root, "launch-profile.enabled")
	marker, err := os.OpenFile(markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		marker.Close()
		defer os.Remove(markerPath)
	} else if !os.IsExist(err) {
		return nil, nil, receipt, err
	}
	profilePath := filepath.Join(root, "launch-profile.jsonl")
	var offset int64
	if info, err := os.Stat(profilePath); err == nil {
		offset = info.Size()
	}
	command := exec.CommandContext(ctx, "/bin/zsh", "-f", "-c", "exec codex --no-alt-screen")
	command.Dir = directory
	command.Env = append(os.Environ(), "PATH="+shims+":"+paths, "TERM=xterm-256color", "NO_COLOR=", "FENNEC_DEBUG=", "FENNEC_COLD_START=0")
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return nil, nil, receipt, err
	}
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		file, err := os.Open(profilePath)
		if err == nil {
			file.Seek(offset, io.SeekStart)
			data, _ := io.ReadAll(io.LimitReader(file, 1<<20))
			file.Close()
			for _, line := range bytes.Split(data, []byte{'\n'}) {
				var event struct {
					PID     int    `json:"pid"`
					Stage   string `json:"stage"`
					Session string `json:"session_id"`
				}
				if json.Unmarshal(line, &event) != nil || event.PID != command.Process.Pid || event.Stage != "stream.receipt" {
					continue
				}
				receipt, err = launcher.ReadReceipt(filepath.Join(root, "launches"), event.Session)
				if err == nil {
					return command, terminal, receipt, nil
				}
			}
		}
		select {
		case <-ticker.C:
			continue
		case <-ctx.Done():
		case <-deadline.C:
		}
		// pty.Start gives this owned client its own process group. Terminate
		// descendants too; otherwise a surviving child can hold the slave open
		// and make diagnostic ReadAll block forever after the receipt deadline.
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		command.Process.Kill()
		// Darwin can leave a killed process in kernel teardown with wait4
		// blocked. Failure diagnostics must remain bounded in that case too.
		reaped := make(chan struct{})
		go func() { command.Wait(); close(reaped) }()
		select {
		case <-reaped:
		case <-time.After(250 * time.Millisecond):
		}
		// Preserve the failure output for diagnosis instead of reporting only a
		// missing receipt. Killing the local client never retries a remote launch.
		captured := make(chan []byte, 1)
		go func() {
			failure, _ := io.ReadAll(io.LimitReader(terminal, 64<<10))
			captured <- failure
		}()
		var failure []byte
		select {
		case failure = <-captured:
		case <-time.After(250 * time.Millisecond):
		}
		terminal.Close()
		return nil, nil, receipt, fmt.Errorf("shim launch outcome unknown for CLI pid %d; inspect its launch profile before retrying; terminal: %s", command.Process.Pid, ansi.ReplaceAll(failure, nil))
	}
}
