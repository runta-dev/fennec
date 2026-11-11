// profile-model-ops measures installed, read-only control paths and fails every
// error, unusable response or individual sample over the requested wall budget.
package main

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	method := flag.String("method", "model.get", "model.get, model.local.get or agent.account.get")
	agent := flag.String("agent", "codex", "agent to inspect")
	location := flag.String("location", "cloud", "account location")
	samples := flag.Int("samples", 5, "number of samples")
	budget := flag.Duration("budget", time.Second, "hard limit for each sample")
	flag.Parse()
	if !map[string]bool{"model.get": true, "model.local.get": true, "agent.account.get": true}[*method] || *samples < 1 || *samples > 100 || *budget <= 0 || (*location != "local" && *location != "cloud") {
		return fmt.Errorf("invalid profiling options")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	client := control.Client{Socket: filepath.Join(home, ".config/fennec/ipc/control.sock")}
	type daemon struct {
		PID   int    `json:"pid"`
		Build string `json:"build_id"`
	}
	ping := func() (daemon, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var value daemon
		err := client.Call(ctx, "daemon.ping", nil, &value)
		return value, err
	}
	original, err := ping()
	if err != nil {
		return err
	}
	if original.PID <= 0 || original.Build == "" {
		return fmt.Errorf("daemon identity unavailable")
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"started_at": time.Now().UTC().Format(time.RFC3339Nano), "daemon_build": original.Build, "daemon_pid": original.PID})
	type connection struct {
		Ready      bool   `json:"ready"`
		Generation uint64 `json:"generation"`
	}
	check := func() (connection, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var state connection
		err := client.Call(ctx, "connection.status", nil, &state)
		return state, err
	}
	before, err := check()
	if err != nil {
		return err
	}
	remote := *method == "model.get" || *method == "agent.account.get" && *location == "cloud"
	if remote && !before.Ready {
		return fmt.Errorf("warm channel is not ready")
	}
	for index := 0; index < *samples; index++ {
		ctx, cancel := context.WithTimeout(context.Background(), *budget)
		var result operationResult
		started := time.Now()
		err := client.Call(ctx, *method, map[string]any{"agent": *agent, "location": *location, "refresh": true}, &result)
		elapsed := time.Since(started)
		cancel()
		if err != nil {
			return fmt.Errorf("sample %d failed: %w", index+1, err)
		}
		if err := result.validate(*method, *agent, *location); err != nil {
			return fmt.Errorf("sample %d: %w", index+1, err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"method": *method, "sample": index + 1, "wall_ms": float64(elapsed.Microseconds()) / 1000, "budget_ms": float64(budget.Microseconds()) / 1000})
		if elapsed > *budget {
			return fmt.Errorf("sample %d exceeded wall-time budget", index+1)
		}
		// Validate every sample, including intermediate reconnects that could be
		// hidden by a later successful response in the same batch.
		currentConnection, err := check()
		if err != nil {
			return err
		}
		if remote && (!currentConnection.Ready || currentConnection.Generation != before.Generation) {
			return fmt.Errorf("sample %d: resident channel changed during warm measurement", index+1)
		}
		currentDaemon, err := ping()
		if err != nil {
			return err
		}
		if currentDaemon != original {
			return fmt.Errorf("sample %d: daemon changed during measurement", index+1)
		}
	}
	after, err := check()
	if err != nil {
		return err
	}
	if remote && (!after.Ready || after.Generation != before.Generation) {
		return fmt.Errorf("resident channel changed during warm measurement")
	}
	current, err := ping()
	if err != nil {
		return err
	}
	if current != original {
		return fmt.Errorf("daemon changed during measurement")
	}
	return nil
}
