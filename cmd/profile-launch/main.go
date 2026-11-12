package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/launcher"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/creack/pty"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	binary := flag.String("binary", "", "Fennec daemon binary")
	mode := flag.String("mode", "cold", "cold, warm, or daemon-cold (restarts the local daemon; close the UI first)")
	samples := flag.Int("samples", 3, "samples")
	invalidateConfiguration := flag.Bool("invalidate-configuration", false, "force configuration reconciliation while retaining installation state (daemon-cold only)")
	flag.Parse()
	if !filepath.IsAbs(*binary) || *samples < 1 || (*mode != "cold" && *mode != "warm" && *mode != "daemon-cold") {
		return fmt.Errorf("binary and cold/warm/daemon-cold mode required")
	}
	if *invalidateConfiguration && *mode != "daemon-cold" {
		return fmt.Errorf("configuration invalidation requires daemon-cold mode")
	}
	if *mode == "daemon-cold" {
		pattern := "^" + regexp.QuoteMeta(filepath.Join(filepath.Dir(*binary), "Fennec")) + "( |$)"
		err := exec.Command("/usr/bin/pgrep", "-f", pattern).Run()
		if err == nil {
			return fmt.Errorf("close the Fennec UI before daemon-cold profiling; its poller can pre-start the daemon")
		}
		if status, ok := err.(*exec.ExitError); !ok || status.ExitCode() != 1 {
			return fmt.Errorf("cannot verify Fennec UI is closed: %w", err)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".config/fennec")
	markerPath := filepath.Join(root, "launch-profile.enabled")
	marker, err := os.OpenFile(markerPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(marker.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		marker.Close()
		return fmt.Errorf("another launch profiler is active: %w", err)
	}
	defer func() {
		_ = os.Remove(markerPath)
		_ = syscall.Flock(int(marker.Fd()), syscall.LOCK_UN)
		_ = marker.Close()
	}()
	identity, err := readDaemonIdentity(root)
	if err != nil {
		return err
	}
	for sample := 0; sample < *samples; sample++ {
		if *mode == "daemon-cold" {
			stop, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err := exec.CommandContext(stop, *binary, "fen", "daemon", "stop").Run()
			cancel()
			if err != nil {
				return fmt.Errorf("cannot stop daemon before measurement: %w", err)
			}
			if _, err := readDaemonIdentity(root); err == nil {
				return fmt.Errorf("daemon remained available; cold measurement invalid")
			}
			if *invalidateConfiguration {
				if err := invalidateConfigurationCheckpoint(root); err != nil {
					return err
				}
			}
		}
		if err := measure(*binary, *mode, root, sample+1); err != nil {
			return err
		}
		if *invalidateConfiguration {
			var cache struct {
				Result string `json:"result"`
			}
			data, err := os.ReadFile(filepath.Join(root, "preparation-cache-status.json"))
			if err != nil || json.Unmarshal(data, &cache) != nil || cache.Result != "installation_hit_configuration_changed" {
				return fmt.Errorf("configuration-only preparation path was not verified")
			}
		}
		current, err := readDaemonIdentity(root)
		if err != nil {
			return err
		}
		if *mode == "daemon-cold" {
			if current.BuildID != identity.BuildID || current.PID == identity.PID {
				return fmt.Errorf("INVALID MEASUREMENT: expected a new daemon with the same build")
			}
			identity = current
		} else if current != identity {
			return fmt.Errorf("INVALID MEASUREMENT: daemon changed during startup profiling")
		}
	}
	return nil
}

type daemonIdentity struct {
	PID     int    `json:"pid"`
	BuildID string `json:"build_id"`
}

func readDaemonIdentity(root string) (daemonIdentity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var identity daemonIdentity
	err := (control.Client{Socket: filepath.Join(root, "ipc/control.sock")}).Call(ctx, "daemon.ping", nil, &identity)
	if err != nil {
		return identity, err
	}
	if identity.PID <= 0 || identity.BuildID == "" {
		return identity, fmt.Errorf("cannot identify daemon for startup profiling")
	}
	return identity, nil
}

func measure(binary, mode, root string, sample int) error {
	directory, err := os.MkdirTemp("", "fennec-launch-profile-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "run-agent", "codex", "--no-alt-screen")
	cold := "0"
	if mode == "cold" {
		cold = "1"
	}
	command.Env = append(os.Environ(), "TERM=xterm-256color", "NO_COLOR=", "FENNEC_DEBUG=", "FENNEC_COLD_START="+cold)
	command.Dir = directory
	started := time.Now()
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 40, Cols: 120})
	spawnElapsed := time.Since(started)
	if err != nil {
		return err
	}
	defer terminal.Close()
	var output bytes.Buffer
	buffer := make([]byte, 32768)
	ansi := regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")
	responses := map[string]string{"\x1b]10;?": "\x1b]10;rgb:cccc/cccc/cccc\x1b\\", "\x1b]11;?": "\x1b]11;rgb:1818/1818/1818\x1b\\", "\x1b]12;?": "\x1b]12;rgb:cccc/cccc/cccc\x1b\\", "\x1b[16t": "\x1b[6;16;8t", "\x1b[c": "\x1b[?1;2c", "\x1b[6n": "\x1b[1;1R"}
	screen := time.Duration(0)
	spinner := false
	firstOutput := time.Duration(0)
	outputBytes := 0
	for {
		length, readErr := terminal.Read(buffer)
		if length > 0 {
			if firstOutput == 0 {
				firstOutput = time.Since(started)
			}
			outputBytes += length
			output.Write(buffer[:length])
			spinner = spinner || bytes.Contains(output.Bytes(), []byte("\r\x1b[2K\x1b[2m⠋\x1b[0m"))
			if output.Len() > 1<<20 {
				tail := append([]byte(nil), output.Bytes()[output.Len()-(1<<19):]...)
				output.Reset()
				output.Write(tail)
			}
			for query, response := range responses {
				if bytes.Contains(buffer[:length], []byte(query)) {
					terminal.Write([]byte(response))
				}
			}
			if screen == 0 {
				visible := output.Bytes()
				if len(visible) > 65536 {
					visible = visible[len(visible)-65536:]
				}
				plain := ansi.ReplaceAll(visible, nil)
				if bytes.Contains(plain, []byte("›")) && bytes.Contains(plain, []byte("Ask Codex")) {
					screen = time.Since(started)
					terminal.Write([]byte{4})
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	waitErr := command.Wait()
	if waitErr != nil || screen == 0 {
		diagnostics := filepath.Join(root, "diagnostics")
		if err := os.MkdirAll(diagnostics, 0700); err != nil {
			return err
		}
		path := filepath.Join(diagnostics, fmt.Sprintf("profile-launch-%d.txt", command.Process.Pid))
		if err := os.WriteFile(path, output.Bytes(), 0600); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Owned test terminal diagnostics: %s\n", path)
	}
	var events []map[string]any
	daemon, daemonErr := readDaemonIdentity(root)
	var sshStartedMS float64
	file, err := os.Open(filepath.Join(root, "launch-profile.jsonl"))
	if err == nil {
		decoder := json.NewDecoder(file)
		for {
			var event map[string]any
			err := decoder.Decode(&event)
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			if event["pid"] == float64(command.Process.Pid) {
				events = append(events, event)
			}
			if daemonErr == nil && event["pid"] == float64(daemon.PID) && event["stage"] == "ssh.resolve_and_connect" {
				if stamp, ok := event["started_unix_ms"].(float64); ok {
					sshStartedMS = stamp
				}
			}
		}
		file.Close()
	}
	// Ctrl-D may detach while preserving the remote agent. Clean up only the
	// identity recorded for this exact profiler child, after validating its receipt.
	for _, event := range events {
		id, ok := event["session_id"].(string)
		if !ok || (event["stage"] != "stream.receipt" && event["stage"] != "direct.receipt") {
			continue
		}
		receipt, err := launcher.ReadReceipt(filepath.Join(root, "launches"), id)
		if err != nil {
			return err
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		var result map[string]any
		err = (control.Client{Socket: filepath.Join(root, "ipc/control.sock")}).Call(cleanup, "sessions.stop", map[string]string{"session_id": id, "address": receipt.Address, "expected_runtime_id": receipt.RuntimeID}, &result)
		cancel()
		if err != nil || result["state"] != "finished" {
			return fmt.Errorf("owned profile session %s cleanup not confirmed: %v", id, err)
		}
		break
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"mode": mode, "sample": sample, "screen_ms": float64(screen.Microseconds()) / 1000, "exit_code": command.ProcessState.ExitCode(), "spinner_visible": spinner, "events": events, "spawn_ms": float64(spawnElapsed.Microseconds()) / 1000, "first_output_ms": float64(firstOutput.Microseconds()) / 1000, "terminal_output_bytes": outputBytes, "profiler_pid": os.Getpid(), "command_started_unix_ms": started.UnixMilli(), "startup_ssh_started_unix_ms": sshStartedMS, "daemon_pid": daemon.PID})
	if waitErr != nil {
		return fmt.Errorf("test launch did not finish: %w; inspect its receipt before retrying", waitErr)
	}
	if screen == 0 {
		return fmt.Errorf("usable composer was not observed")
	}
	if !spinner {
		return fmt.Errorf("Fennec startup animation was not observed before TUI takeover")
	}
	if mode == "daemon-cold" && (daemonErr != nil || sshStartedMS < float64(started.UnixMilli())) {
		return fmt.Errorf("INVALID MEASUREMENT: fresh daemon SSH startup after command invocation was not verified")
	}
	return checkLaunchBudget(mode, screen)
}

func checkLaunchBudget(mode string, elapsed time.Duration) error {
	limit := 500 * time.Millisecond
	if mode == "cold" || mode == "daemon-cold" {
		limit = 5 * time.Second
	}
	if elapsed > limit {
		return fmt.Errorf("PERFORMANCE REGRESSION: %s usable TUI took %s, exceeds %s", mode, elapsed, limit)
	}
	return nil
}
