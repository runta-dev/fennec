// verify-control exercises the installed UI-independent control plane. It never
// changes user credentials/trust or sends input to existing user sessions.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	binary := flag.String("binary", "", "installed daemon path")
	flag.Parse()
	if *binary == "" {
		return fmt.Errorf("binary required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli := func(params any, args ...string) (map[string]any, error) {
		command := exec.CommandContext(ctx, *binary, append([]string{"fen"}, args...)...)
		if params != nil {
			data, _ := json.Marshal(params)
			command.Stdin = bytes.NewReader(data)
		}
		raw, err := command.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return nil, fmt.Errorf("%s: %s", args[0], strings.TrimSpace(string(exit.Stderr)))
			}
			return nil, err
		}
		var result map[string]any
		if json.Unmarshal(raw, &result) != nil {
			return nil, fmt.Errorf("invalid %s result", args[0])
		}
		return result, nil
	}
	if exec.Command("/usr/bin/pgrep", "-f", "/Fennec.app/Contents/MacOS/Fennec$").Run() == nil {
		return fmt.Errorf("close the native UI before this acceptance check")
	}
	initial, err := cli(nil, "daemon", "status")
	if err != nil {
		return err
	}
	pid := initial["pid"]
	account, err := cli(nil, "control", "account.status")
	if err != nil || account["status"] != "authorized" {
		return fmt.Errorf("credential restoration not confirmed: %v", err)
	}
	state, err := cli(nil, "status")
	if err != nil {
		return err
	}
	runtime, _ := state["runtime"].(map[string]any)
	runtimeID, _ := runtime["runtime_id"].(string)
	services, _ := state["services"].(map[string]any)
	connector, _ := services["connector"].(map[string]any)
	if runtimeID == "" || services["broker_ready"] != true || connector["stage"] != "connected" {
		return fmt.Errorf("daemon services not ready")
	}
	settings, err := cli(nil, "settings")
	if err != nil {
		return err
	}
	if language, ok := settings["language"].(string); ok {
		if _, err := cli(nil, "settings", "language", language); err != nil {
			return err
		}
	}
	if _, err := cli(nil, "account"); err != nil {
		return err
	}
	if _, err := cli(nil, "login-item", "status"); err != nil {
		return err
	}
	// Reapply the existing takeover selection; this must not replace the broker.
	selected, _ := services["selected"].([]any)
	if len(selected) > 0 {
		if _, err := cli(nil, "takeover", selected[0].(string), "on"); err != nil {
			return err
		}
	}
	home, _ := os.UserHomeDir()
	cloud := filepath.Join(home, ".config/fennec/cloud")
	sshArgs := []string{"-T", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + filepath.Join(cloud, "known_hosts"), "-i", filepath.Join(cloud, "runtime_key"), runtimeID + "@ssh.runta.com"}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(nonce[:])
	// Only fixture creation uses a separate SSH call; every measured operation is
	// an actual installed CLI command through the resident daemon channel.
	create := exec.CommandContext(ctx, "/usr/bin/ssh", append(sshArgs, "/root/.local/bin/herdr-core run --json --bg --no-parent --no-notify --label fennec.session="+id+" --label fennec.agent=codex -- /usr/bin/timeout 120 /bin/cat")...)
	raw, err := create.Output()
	if err != nil {
		return fmt.Errorf("fixture creation: %w", err)
	}
	var receipt struct {
		Address string `json:"address"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Address == "" {
		return fmt.Errorf("invalid fixture receipt")
	}
	params := map[string]any{"session_id": id, "address": receipt.Address}
	marker := "fennec-control-check-" + id[:8]
	send := map[string]any{"session_id": id, "address": receipt.Address, "text": marker}
	timings := map[string]float64{}
	start := time.Now()
	if _, err = cli(send, "control", "sessions.send"); err != nil {
		return err
	}
	timings["send_ms"] = float64(time.Since(start).Microseconds()) / 1000
	start = time.Now()
	read, err := cli(params, "control", "sessions.read")
	if err != nil {
		return err
	}
	timings["read_ms"] = float64(time.Since(start).Microseconds()) / 1000
	encoded, _ := json.Marshal(read)
	if !bytes.Contains(encoded, []byte(marker)) {
		return fmt.Errorf("sent input did not reach fixture terminal")
	}
	start = time.Now()
	stopped, err := cli(params, "control", "sessions.stop")
	if err != nil || stopped["state"] != "finished" {
		return fmt.Errorf("fixture stop unconfirmed: %v", err)
	}
	timings["stop_ms"] = float64(time.Since(start).Microseconds()) / 1000
	listing, err := cli(nil, "sessions")
	if err != nil {
		return err
	}
	rows, _ := listing["terminals"].([]any)
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		labels, _ := row["labels"].(map[string]any)
		if labels["fennec.session"] == id && row["state"] == "live" {
			return fmt.Errorf("stale stopped session in cache")
		}
	}
	final, err := cli(nil, "daemon", "status")
	if err != nil || final["pid"] != pid {
		return fmt.Errorf("daemon changed during CLI operations")
	}
	if exec.Command("/usr/bin/pgrep", "-f", "/Fennec.app/Contents/MacOS/Fennec$").Run() == nil {
		return fmt.Errorf("an operation launched the native UI")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"ui_closed": true, "credentials_restored": true, "connector_connected": true, "daemon_pid_unchanged": pid, "settings_and_takeover_shared": true, "literal_input_verified": true, "stop_confirmed": true, "timings": timings})
}
