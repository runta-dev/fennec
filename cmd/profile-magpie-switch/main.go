// profile-magpie-switch measures applied cloud configuration, not settings receipts.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fennec/internal/control"
	"fennec/internal/controlplane"
)

type preferences struct {
	Magpie   bool   `json:"magpie"`
	Revision uint64 `json:"revision"`
}
type snapshot struct {
	Settings preferences `json:"settings"`
	Account  struct {
		Epoch  uint64 `json:"epoch"`
		Status string `json:"status"`
	} `json:"account"`
	Runtime controlplane.RuntimeState `json:"runtime"`
}
type identity struct {
	PID   int    `json:"pid"`
	Build string `json:"build_id"`
}
type connection struct {
	Ready      bool   `json:"ready"`
	Generation uint64 `json:"generation"`
}
type probe struct {
	client interface {
		Call(context.Context, string, any, any) error
	}
	daemon  identity
	channel connection
	initial snapshot
}

func (p *probe) call(method string, params, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.client.Call(ctx, method, params, out)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() (result error) {
	live := flag.Bool("live", false, "explicitly switch and restore the live cloud configuration")
	runtimeID := flag.String("runtime", "", "required expected cloud runtime ID")
	build := flag.String("build", "", "required expected installed daemon build ID")
	budget := flag.Duration("budget", time.Second, "per-switch applied-configuration budget")
	flag.Parse()
	if !*live || *runtimeID == "" || *build == "" || *budget <= 0 {
		return fmt.Errorf("require -live -runtime <ID> -build <build ID> and positive budget")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	p := probe{client: control.Client{Socket: filepath.Join(home, ".config/fennec/ipc/control.sock")}}
	if err = p.call("daemon.ping", nil, &p.daemon); err != nil {
		return err
	}
	if p.daemon.Build != *build || p.daemon.PID <= 0 {
		return fmt.Errorf("daemon identity mismatch")
	}
	if err = p.call("connection.status", nil, &p.channel); err != nil {
		return err
	}
	if !p.channel.Ready {
		return fmt.Errorf("warm channel is not ready")
	}
	if err = p.call("state.get", nil, &p.initial); err != nil {
		return err
	}
	if p.initial.Account.Status != "authorized" || p.initial.Runtime.RuntimeID != *runtimeID || !settled(p.initial.Runtime) || p.initial.Runtime.Generation == 0 {
		return fmt.Errorf("require an authorized, settled runtime with configuration generation support")
	}
	baseline, err := routeBaseline(p.initial)
	if err != nil {
		return fmt.Errorf("initial route baseline unavailable: %w", err)
	}
	report := json.NewEncoder(os.Stdout)
	if err = report.Encode(map[string]any{"started_at": time.Now().UTC(), "daemon_build": p.daemon.Build, "daemon_pid": p.daemon.PID, "runtime_id": *runtimeID, "initial_magpie": p.initial.Settings.Magpie, "settings_revision": p.initial.Settings.Revision}); err != nil {
		return err
	}
	if err = report.Encode(map[string]any{"initial_routes": baseline}); err != nil {
		return err
	}
	return p.measure(report, *budget)
}

func (p *probe) measure(report *json.Encoder, budget time.Duration) (result error) {
	started := time.Now()
	var changed preferences
	if err := p.call("settings.set", map[string]any{"magpie": !p.initial.Settings.Magpie, "expected_revision": p.initial.Settings.Revision}, &changed); err != nil {
		return fmt.Errorf("switch outcome unconfirmed; inspect settings before another mutation: %w", err)
	}
	if changed.Magpie == p.initial.Settings.Magpie || changed.Revision != p.initial.Settings.Revision+1 {
		return fmt.Errorf("unexpected settings receipt; inspect before restoring")
	}
	// Restoration is guarded by the exact revision we wrote, so a later user
	// choice (including off/on ABA) is never overwritten by this tool.
	defer func() {
		beforeRestore, err := p.observe(changed)
		if err != nil {
			result = errors.Join(result, fmt.Errorf("restoration not attempted after identity/preference change: %w", err))
			return
		}
		restoreStarted := time.Now()
		var restored preferences
		err = p.call("settings.set", map[string]any{"magpie": p.initial.Settings.Magpie, "expected_revision": changed.Revision}, &restored)
		if err != nil {
			result = errors.Join(result, fmt.Errorf("restoration unconfirmed: %w", err))
			return
		}
		if restored.Magpie != p.initial.Settings.Magpie || restored.Revision != changed.Revision+1 {
			result = errors.Join(result, fmt.Errorf("unexpected restoration receipt"))
			return
		}
		state, err := p.wait(restored, beforeRestore.Runtime.Generation)
		elapsed := time.Since(restoreStarted)
		if err == nil {
			err = validateRestoration(state, p.initial)
		}
		if writeErr := report.Encode(map[string]any{"operation": "restore", "wall_ms": float64(elapsed.Microseconds()) / 1000, "confirmed": err == nil, "phases": phaseEvidence(state), "catalog": catalogEvidence(state)}); writeErr != nil {
			err = errors.Join(err, writeErr)
		}
		if err == nil && elapsed > budget {
			err = fmt.Errorf("restore exceeded %s budget", budget)
		}
		result = errors.Join(result, err)
	}()
	state, err := p.wait(changed, p.initial.Runtime.Generation)
	elapsed := time.Since(started)
	if err == nil {
		err = validateCompletion(state, changed.Magpie)
	}
	if writeErr := report.Encode(map[string]any{"operation": "switch", "wall_ms": float64(elapsed.Microseconds()) / 1000, "confirmed": err == nil, "phases": phaseEvidence(state), "catalog": catalogEvidence(state)}); writeErr != nil {
		err = errors.Join(err, writeErr)
	}
	if err != nil {
		return err
	}
	if elapsed > budget {
		return fmt.Errorf("switch exceeded %s budget", budget)
	}
	return nil
}
func settled(r controlplane.RuntimeState) bool {
	return !r.Running && !r.Pending && r.Stage == "ready" && r.Error == ""
}
func (p *probe) observe(want preferences) (snapshot, error) {
	var state snapshot
	var daemon identity
	var channel connection
	if err := p.call("daemon.ping", nil, &daemon); err != nil {
		return state, err
	}
	if daemon != p.daemon {
		return state, fmt.Errorf("daemon changed")
	}
	if err := p.call("connection.status", nil, &channel); err != nil {
		return state, err
	}
	if !channel.Ready || channel.Generation != p.channel.Generation {
		return state, fmt.Errorf("resident channel changed")
	}
	if err := p.call("state.get", nil, &state); err != nil {
		return state, err
	}
	if state.Account.Epoch != p.initial.Account.Epoch || state.Account.Status != "authorized" {
		return state, fmt.Errorf("account changed")
	}
	if state.Runtime.RuntimeID != "" && state.Runtime.RuntimeID != p.initial.Runtime.RuntimeID {
		return state, fmt.Errorf("runtime changed")
	}
	if state.Settings != want {
		return state, fmt.Errorf("settings superseded")
	}
	return state, nil
}
func (p *probe) wait(want preferences, previous uint64) (snapshot, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		state, err := p.observe(want)
		if err != nil {
			return state, err
		}
		if state.Runtime.Stage == "failed" && !state.Runtime.Running && !state.Runtime.Pending {
			return state, fmt.Errorf("configuration failed")
		}
		if state.Runtime.Generation > previous && settled(state.Runtime) {
			return state, nil
		}
		if time.Now().After(deadline) {
			return state, fmt.Errorf("configuration outcome unconfirmed after observation deadline")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
func validateCompletion(state snapshot, magpie bool) error {
	if !settled(state.Runtime) || state.Runtime.RuntimeID == "" {
		return fmt.Errorf("runtime not settled")
	}
	event := state.Runtime.Events["route_results"]
	if event["runtime_id"] != state.Runtime.RuntimeID {
		return fmt.Errorf("route runtime mismatch")
	}
	var routes []struct {
		Agent  string `json:"agent"`
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(event["results"]), &routes) != nil {
		return fmt.Errorf("route readback unavailable")
	}
	wanted := map[string]bool{"codex": true, "claude": true, "pi": true}
	for _, route := range routes {
		if !wanted[route.Agent] {
			return fmt.Errorf("unexpected or duplicate route")
		}
		delete(wanted, route.Agent)
		if route.Status != "configured" && (magpie || route.Status != "credentials_required") {
			return fmt.Errorf("agent route is not usable")
		}
	}
	if len(wanted) != 0 {
		return fmt.Errorf("incomplete route readback")
	}
	if magpie {
		event = state.Runtime.Events["model_catalog"]
		var models []string
		if event["runtime_id"] != state.Runtime.RuntimeID || json.Unmarshal([]byte(event["models"]), &models) != nil || len(models) == 0 {
			return fmt.Errorf("post-configuration model catalog unavailable")
		}
		for _, model := range models {
			if model == "" {
				return fmt.Errorf("invalid model catalog")
			}
		}
	}
	return nil
}
