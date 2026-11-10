package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/agent"
	"fennec/internal/launchprofile"
	"fennec/internal/runta"
	"fennec/internal/setup"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// prepareRuntime is shared by the standalone daemon and the legacy command.
// Its cancellation belongs to the caller's account lifecycle.
func prepareRuntime(ctx context.Context, token string, output io.Writer) error {
	defer launchprofile.Begin("prepare.total")()
	ctx, cancelPreparation := context.WithCancel(ctx)
	defer cancelPreparation()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	versions := []setup.Agent{}
	ids := []string{}
	for _, id := range []string{"claude", "codex", "pi", "opencode"} {
		if _, err := agent.EntryPath(id); err == nil {
			versions = append(versions, setup.Agent{ID: id, Version: "latest"})
			ids = append(ids, id)
		}
	}
	localRoutes := make([]setup.AgentRoute, len(versions))
	localReady := make(chan struct{})
	go func() {
		defer close(localReady)
		defer launchprofile.Begin("prepare.local_routes")()
		var workers sync.WaitGroup
		for i, version := range versions {
			workers.Add(1)
			go func(i int, version setup.Agent) {
				defer workers.Done()
				localRoutes[i] = setup.LocalAgentRoutes(ctx, []setup.Agent{version})[0]
			}(i, version)
		}
		workers.Wait()
	}()
	var recorded bytes.Buffer
	output = io.MultiWriter(output, &recorded)
	client := &runta.Client{Endpoint: "https://api.runta.com", Token: token}
	emit := func(stage, id, status string) {
		json.NewEncoder(output).Encode(map[string]string{"type": "runtime_progress", "stage": stage, "runtime_id": id, "status": status})
	}
	emit("allocating", "", "")
	runtime, err := client.EnsureShared(ctx)
	if err != nil {
		emit("failed", "", err.Error())
		return err
	}
	switch runtime.Status {
	case "shutdown", "stopped":
		err = client.Start(ctx, runtime.ID)
	case "paused", "suspended":
		err = client.Resume(ctx, runtime.ID)
	}
	if err != nil {
		emit("failed", runtime.ID, err.Error())
		return err
	}
	if runtime.Status != "running" || runtime.Degraded || !runtime.SSHEnabled {
		err = client.WaitReady(ctx, runtime.ID, func(r runta.Runtime) { emit("starting", r.ID, r.Status) })
	}
	if err != nil {
		emit("failed", runtime.ID, err.Error())
		return err
	}
	emit("connecting", runtime.ID, "")
	connection, err := client.Connect(ctx, runtime.ID, filepath.Join(home, ".config", "fennec", "cloud"))
	if err != nil {
		emit("failed", runtime.ID, err.Error())
		return err
	}
	defer connection.Close()
	// The local connector needs authenticated runtime SSH, not agent or
	// model readiness. Publish this independently so model setup failures
	// cannot prevent the user's computer from reconnecting.
	select {
	case <-localReady:
	case <-ctx.Done():
		return ctx.Err()
	}
	// EnsureShared already authenticated and matched this owner against /v2/me.
	routes, err := setup.ApplyModelPreferences(filepath.Join(home, ".config", "fennec"), runtime.Owner, localRoutes)
	if err != nil {
		return err
	}
	plugins, err := setup.DesiredModelPlugins(home)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	artifacts := filepath.Join(filepath.Dir(executable), "..", "Resources")
	fingerprint, fingerprintErr := preparationFingerprint(home, token, runtime.ID, []any{versions, routes, plugins}, artifacts)
	cacheStatus := func(result string) {
		data, _ := json.Marshal(map[string]string{"result": result})
		_ = os.WriteFile(filepath.Join(home, ".config/fennec/preparation-cache-status.json"), data, 0600)
	}
	cacheStatus("inputs_changed_or_no_checkpoint")
	if fingerprintErr == nil {
		if checkpoint, ok := readPreparationCheckpoint(home, fingerprint); ok {
			check, cancel := context.WithTimeout(ctx, 5*time.Second)
			healthDone := launchprofile.Begin("prepare.health")
			err := setup.Exec(check, connection, checkpointHealthScript(fingerprint, ids))
			healthDone()
			cancel()
			if err == nil {
				cacheStatus("hit")
				json.NewEncoder(output).Encode(map[string]string{"type": "connector_ready", "runtime_id": runtime.ID})
				for _, event := range checkpoint.Events {
					json.NewEncoder(output).Encode(event)
				}
				return nil
			}
			cacheStatus("remote_health_failed")
		}
	} else {
		cacheStatus("fingerprint_failed")
	}
	if err := setup.Exec(ctx, connection, "test -d / && command -v sh >/dev/null"); err != nil {
		return err
	}
	json.NewEncoder(output).Encode(map[string]string{"type": "connector_ready", "runtime_id": runtime.ID})
	emit("checking_resources", runtime.ID, "")
	if err = setup.Exec(ctx, connection, setup.GuestCapacityScript); err != nil {
		emit("failed", runtime.ID, "cloud resource allocation has not taken effect")
		return err
	}
	emit("syncing_versions", runtime.ID, "")
	if err = setup.SyncAgentManifest(ctx, connection, versions); err != nil {
		emit("failed", runtime.ID, "agent version manifest sync failed")
		return err
	}
	emit("installing_node", runtime.ID, "")
	if err = setup.Exec(ctx, connection, setup.NodeSetupScript); err != nil {
		emit("failed", runtime.ID, "Node setup failed")
		return err
	}
	emit("installing_session_service", runtime.ID, "")
	if err = setup.Exec(ctx, connection, setup.HerdrSetupScript); err != nil {
		emit("failed", runtime.ID, "session service reconciliation failed")
		return err
	}
	if err = setup.InstallTerminalBinary(ctx, connection, filepath.Join(filepath.Dir(executable), "..", "Resources")); err != nil {
		emit("failed", runtime.ID, "Go terminal service installation failed")
		return err
	}
	emit("installing_magpie", runtime.ID, "")
	if err = setup.Exec(ctx, connection, setup.MagpieSetupScript); err != nil {
		emit("failed", runtime.ID, "Magpie installation failed")
		return err
	}
	emit("starting_magpie", runtime.ID, "")
	if err = setup.Exec(ctx, connection, setup.MagpieServiceScript); err != nil {
		emit("failed", runtime.ID, "Magpie service failed: "+err.Error())
		return err
	}
	emit("migrating_credentials", runtime.ID, "")
	migrated, err := setup.MigrateMagpieCredentials(ctx, connection, home)
	if err != nil {
		emit("failed", runtime.ID, err.Error())
		return err
	}
	emit("installing_model_plugins", runtime.ID, "")
	if err = setup.ReconcileModelPlugins(ctx, connection, plugins); err != nil {
		emit("failed", runtime.ID, "model plugin reconciliation failed")
		return err
	}
	emit("restoring_model_accounts", runtime.ID, "")
	if err = setup.Exec(ctx, connection, setup.RestoreCodexIdentityScript); err != nil {
		emit("failed", runtime.ID, "model account restoration failed")
		return err
	}
	models, catalogErr := setup.GatewayModels(ctx, connection)
	if catalogErr == nil {
		data, _ := json.Marshal(models)
		json.NewEncoder(output).Encode(map[string]string{"type": "model_catalog", "models": string(data)})
	}
	results := []setup.RouteResult{}
	json.NewEncoder(output).Encode(map[string]string{"type": "shared_services_ready", "runtime_id": runtime.ID})
	initialResults, _ := json.Marshal(results)
	json.NewEncoder(output).Encode(map[string]string{"type": "route_results", "runtime_id": runtime.ID, "results": string(initialResults)})
	err = prepareAgentsIndependently(ctx, versions, routes,
		func(ctx context.Context, a setup.Agent) error {
			script, err := setup.EnsureAgentScript(a)
			if err != nil {
				return err
			}
			return setup.Exec(ctx, connection, script)
		},
		func(ctx context.Context, route setup.AgentRoute) ([]setup.RouteResult, error) {
			return setup.ReconcileRoutes(ctx, connection, []setup.AgentRoute{route})
		},
		func(one []setup.RouteResult) {
			results = append(results, one...)
			encodedResults, _ := json.Marshal(results)
			json.NewEncoder(output).Encode(map[string]string{"type": "route_results", "runtime_id": runtime.ID, "results": string(encodedResults)})
		})
	if err != nil {
		emit("failed", runtime.ID, err.Error())
		return err
	}

	results, err = recoverRoutes(ctx, routes, results, 30*time.Second,
		func(ctx context.Context, route setup.AgentRoute) ([]setup.RouteResult, error) {
			return setup.ReconcileRoutes(ctx, connection, []setup.AgentRoute{route})
		}, func(current []setup.RouteResult) {
			encoded, _ := json.Marshal(current)
			json.NewEncoder(output).Encode(map[string]string{"type": "route_results", "runtime_id": runtime.ID, "results": string(encoded)})
		})
	if err != nil {
		return err
	}
	status := "routes_configured_inference_pending"
	for _, result := range results {
		if result.Status != "configured" {
			status = "model_routes_need_attention"
			break
		}
	}
	if migrated == 0 {
		status = "no_local_model_credentials"
	}
	cacheable := fingerprintErr == nil && len(results) > 0
	for _, result := range results {
		switch result.Status {
		case "configured", "model_selection_required", "local_gateway_not_found", "unsupported_agent":
		default:
			cacheable = false
		}
	}
	if cacheable {
		checkpoint := preparationCheckpoint{Fingerprint: fingerprint}
		decoder := json.NewDecoder(bytes.NewReader(recorded.Bytes()))
		for {
			var event map[string]string
			if decoder.Decode(&event) != nil {
				break
			}
			if event["type"] == "model_catalog" || event["type"] == "shared_services_ready" || event["stage"] == "agents_installed" {
				checkpoint.Events = append(checkpoint.Events, event)
			}
		}
		encoded, _ := json.Marshal(results)
		checkpoint.Events = append(checkpoint.Events, map[string]string{"type": "route_results", "runtime_id": runtime.ID, "results": string(encoded)}, map[string]string{"type": "runtime_progress", "stage": "agents_installed", "runtime_id": runtime.ID, "status": status})
		// Publish only after complete preparation; a missing marker forces repair.
		if setup.Exec(ctx, connection, "umask 077; mkdir -p \"$HOME/.config/fennec\"; printf '%s' '"+fingerprint+"' > \"$HOME/.config/fennec/prepared-runtime.tmp\" && mv \"$HOME/.config/fennec/prepared-runtime.tmp\" \"$HOME/.config/fennec/prepared-runtime\"") == nil {
			_ = savePreparationCheckpoint(home, checkpoint)
		}
	}
	emit("agents_installed", runtime.ID, status)
	return nil
}
