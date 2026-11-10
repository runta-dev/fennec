// profile-stop measures only its own labeled sleep sessions, never user agents.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	r "fennec/internal/runtime"
	"fennec/internal/terminalstream"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	binary := flag.String("binary", "", "Linux amd64 terminal binary")
	samples := flag.Int("samples", 3, "samples per path")
	paths := flag.String("paths", "fresh_ssh_exec,resident_channel", "comma-separated measurement paths")
	daemon := flag.String("daemon", "", "installed daemon for local_cli measurement")
	flag.Parse()
	if *binary == "" {
		return fmt.Errorf("binary required")
	}
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".config/fennec")
	data, err := os.ReadFile(filepath.Join(root, "preparation.json"))
	if err != nil {
		return err
	}
	var state struct {
		ID string `json:"runtime_id"`
	}
	if json.Unmarshal(data, &state) != nil || state.ID == "" {
		return fmt.Errorf("runtime unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + filepath.Join(root, "cloud/known_hosts"), "-i", filepath.Join(root, "cloud/runtime_key"), state.ID + "@ssh.runta.com"}
	command := func(script string) *exec.Cmd {
		return exec.CommandContext(ctx, "/usr/bin/ssh", append(append([]string{}, args...), script)...)
	}
	nonce := func() string {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic(err)
		}
		return hex.EncodeToString(b[:])
	}
	remote := "/tmp/fennec-stop-profile-" + nonce()
	file, err := os.Open(*binary)
	if err != nil {
		return err
	}
	upload := command("umask 077; cat > " + remote + " && chmod 700 " + remote)
	upload.Stdin = file
	err = upload.Run()
	file.Close()
	if err != nil {
		return err
	}
	defer command("rm -f " + remote).Run()
	transport, err := terminalstream.Start(command(remote + " serve"))
	if err != nil {
		return err
	}
	defer transport.Close()
	if err = transport.Ready(ctx); err != nil {
		return err
	}
	for _, mode := range strings.Split(*paths, ",") {
		if mode != "fresh_ssh_exec" && mode != "resident_channel" && mode != "local_cli" {
			return fmt.Errorf("unknown path")
		}
		if mode == "local_cli" && *daemon == "" {
			return fmt.Errorf("daemon required")
		}
		for i := 0; i < *samples; i++ {
			id := nonce()
			create := command("/root/.local/bin/herdr-core run --json --bg --no-parent --no-notify --label fennec.session=" + id + " --label fennec.agent=codex -- /bin/sleep 120")
			raw, err := create.Output()
			if err != nil {
				return fmt.Errorf("create owned test session: %w", err)
			}
			var terminal r.LocalTerminal
			if json.Unmarshal(raw, &terminal) != nil || terminal.Address == "" {
				return fmt.Errorf("invalid test receipt")
			}
			terminal.ID = id
			if _, err := r.StopScript(terminal.Address, id); err != nil {
				return err
			}
			start := time.Now()
			var result r.LocalTerminal
			if mode == "resident_channel" {
				result, err = transport.Stop(ctx, terminal)
			} else if mode == "local_cli" {
				payload, _ := json.Marshal(map[string]string{"runtime_id": state.ID, "address": terminal.Address, "session_id": id})
				stop := exec.CommandContext(ctx, *daemon, "stop-session")
				stop.Stdin = bytes.NewReader(payload)
				raw, err = stop.Output()
				if exit, ok := err.(*exec.ExitError); ok {
					err = fmt.Errorf("local stop: %s", strings.TrimSpace(string(exit.Stderr)))
				}
				if err == nil {
					err = json.Unmarshal(raw, &result)
				}
			} else {
				raw, err = command(remote + " stop " + terminal.Address + " " + id).Output()
				if err == nil {
					err = json.Unmarshal(raw, &result)
				}
			}
			elapsed := time.Since(start)
			if err != nil {
				return fmt.Errorf("owned test %s stop failed (sleep expires automatically): %w", id, err)
			}
			if result.State != "finished" {
				return fmt.Errorf("test stop not confirmed")
			}
			json.NewEncoder(os.Stdout).Encode(map[string]any{"mode": mode, "sample": i + 1, "wall_ms": float64(elapsed.Microseconds()) / 1000, "guest_ms": result.Timings})
		}
	}
	return nil
}
