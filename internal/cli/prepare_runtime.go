package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/agent"
	"fennec/internal/launchprofile"
	"fennec/internal/runta"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"fennec/internal/terminalstream"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// prepareRuntime is shared by the standalone daemon and the legacy command.
// Its cancellation belongs to the caller's account lifecycle.
type preparationRetainer struct {
	Runtime    func(runta.Runtime) error
	Connection func(string, *ssh.Client) bool
	Terminal   func(string, *terminalstream.Transport) bool
}

func prepareRuntime(ctx context.Context, token string, output io.Writer, retain ...preparationRetainer) error {
	defer launchprofile.Begin("prepare.total")()
	ctx, cancelPreparation := context.WithCancel(ctx)
	defer cancelPreparation()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	magpie := newDesktopSettings(home).value.Magpie
	versions := []setup.Agent{}
	ids := agent.CloudAgentIDs()
	for _, id := range ids {
		versions = append(versions, setup.Agent{ID: id, Version: "latest"})
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
				if magpie {
					localRoutes[i] = setup.LocalAgentRoutes(ctx, []setup.Agent{version})[0]
					if localRoutes[i].Problem == "local_gateway_not_found" {
						localRoutes[i].Problem = "model_selection_required"
					}
				} else {
					localRoutes[i] = setup.AgentRoute{Agent: version.ID}
				}
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
	var hint struct {
		RuntimeID string `json:"runtime_id"`
	}
	if data, err := os.ReadFile(filepath.Join(home, ".config/fennec/takeover-preferences.json")); err == nil && len(data) <= 8192 {
		_ = json.Unmarshal(data, &hint)
	}
	var runtime runta.Runtime
	connection, err := client.ConnectResolved(ctx, hint.RuntimeID, filepath.Join(home, ".config", "fennec", "cloud"), func(ctx context.Context) (string, error) {
		var err error
		runtime, err = client.EnsureShared(ctx)
		if err != nil {
			return "", err
		}
		if len(retain) == 1 && retain[0].Runtime != nil {
			if err := retain[0].Runtime(runtime); err != nil {
				return "", err
			}
		}
		switch runtime.Status {
		case "shutdown", "stopped":
			err = client.Start(ctx, runtime.ID)
		case "paused", "suspended":
			err = client.Resume(ctx, runtime.ID)
		}
		if err != nil {
			return "", err
		}
		if runtime.Status != "running" || runtime.Degraded || !runtime.SSHEnabled {
			err = client.WaitReady(ctx, runtime.ID, func(r runta.Runtime) { emit("starting", r.ID, r.Status) })
		}
		if err != nil {
			return "", err
		}
		emit("connecting", runtime.ID, "")
		return runtime.ID, nil
	})
	if err != nil {
		emit("failed", runtime.ID, err.Error())
		return err
	}
	retained := false
	if len(retain) == 1 && retain[0].Connection != nil {
		retained = retain[0].Connection(runtime.ID, connection)
	}
	defer func() {
		if !retained {
			connection.Close()
		}
	}()
	// The local connector needs authenticated runtime SSH, not agent or
	// model readiness. Publish this independently so model setup failures
	// cannot prevent the user's computer from reconnecting.
	select {
	case <-localReady:
	case <-ctx.Done():
		return ctx.Err()
	}
	// EnsureShared already authenticated and matched this owner against /v2/me.
	routes := localRoutes
	var plugins []setup.ModelPlugin
	sourceIDs := ids
	if !magpie {
		cloudAccounts := setup.NativeCloudPreferences(filepath.Join(home, ".config/fennec"), runtime.Owner, runtime.ID)
		sourceIDs = nil
		for _, id := range ids {
			if !cloudAccounts[id] {
				sourceIDs = append(sourceIDs, id)
			}
		}
	}
	nativeRequest, err := setup.GatherNativeAccounts(ctx, home, sourceIDs, magpie)
	nativeRequest.Agents = ids

	if err != nil {
		return err
	}
	if magpie {
		routes, err = setup.ApplyModelPreferences(filepath.Join(home, ".config", "fennec"), runtime.Owner, localRoutes)
		if err != nil {
			return err
		}
		plugins, err = setup.DesiredModelPlugins(home)
	}
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	artifacts := filepath.Join(filepath.Dir(executable), "..", "Resources")
	var fingerprint string
	var fingerprintErr error
	if magpie {
		fingerprint, fingerprintErr = preparationFingerprint(home, token, runtime.ID, []any{versions, routes, plugins, nativeRequest}, artifacts)
	} else {
		fingerprint, fingerprintErr = scopedPreparationFingerprint(home, token, runtime.ID, []any{versions, nativeRequest}, artifacts, false)
	}
	cacheStatus := func(result string) {
		data, _ := json.Marshal(map[string]string{"result": result})
		_ = os.WriteFile(filepath.Join(home, ".config/fennec/preparation-cache-status.json"), data, 0600)
	}
	cacheStatus("inputs_changed_or_no_checkpoint")
	if fingerprintErr == nil {
		if checkpoint, ok := readPreparationCheckpoint(home, fingerprint); ok {
			check, cancel := context.WithTimeout(ctx, 5*time.Second)
			healthDone := launchprofile.Begin("prepare.health")
			var err error
			if retained && retain[0].Terminal != nil {
				var transport *terminalstream.Transport
				transport, err = terminalstream.StartSSHContext(check, connection, checkpointTerminalCommandForMarker(fingerprint, ids, "prepared-runtime", magpie))
				if err == nil {
					err = transport.Ready(check)
				}
				if err == nil && !retain[0].Terminal(runtime.ID, transport) {
					err = fmt.Errorf("prepared terminal handoff no longer valid")
				}
				if err != nil && transport != nil {
					transport.Close()
				}
			} else {
				err = setup.ExecPublicCheck(check, connection, checkpointHealthScriptForMarker(fingerprint, ids, "prepared-runtime", magpie))
			}
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
	installation, installationErr := installationFingerprint(home, token, runtime.ID, versions, artifacts)
	var resident *terminalstream.Transport
	startResident := func(ctx context.Context, command string) error {
		transport, err := terminalstream.StartSSHContext(ctx, connection, command)
		if err == nil {
			err = transport.Ready(ctx)
		}
		if err == nil && !retain[0].Terminal(runtime.ID, transport) {
			err = fmt.Errorf("prepared terminal handoff no longer valid")
		}
		if err != nil {
			if transport != nil {
				transport.Close()
			}
			return err
		}
		resident = transport
		return nil
	}
	installationReady := false
	if installationErr == nil && installationCheckpointMatches(home, installation) {
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		if retained && retain[0].Terminal != nil {
			installationReady = startResident(check, checkpointTerminalCommandForMarker(installation, nil, "prepared-installation", false)) == nil
		} else {
			installationReady = setup.ExecPublicCheck(check, connection, checkpointHealthScriptForMarker(installation, nil, "prepared-installation", false)) == nil
		}
		cancel()
	}
	if installationReady {
		cacheStatus("installation_hit_configuration_changed")
	}
	if !installationReady {
		if err := setup.Exec(ctx, connection, "test -d / && command -v sh >/dev/null"); err != nil {
			return err
		}
	}
	json.NewEncoder(output).Encode(map[string]string{"type": "connector_ready", "runtime_id": runtime.ID})
	if !installationReady {
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
		if installationErr == nil && setup.ExecPublicCheck(ctx, connection, installationMarkerScript(installation)) == nil {
			_ = writePublicJSON(installationCheckpointPath(home), map[string]string{"fingerprint": installation})
		}
	}
	// Gateway readiness is independent of the shared terminal installation.
	// Toggling integration must never re-upload Herdr or fennec-terminal.
	if magpie {
		emit("installing_magpie", runtime.ID, "")
		if err = setup.Exec(ctx, connection, setup.MagpieSetupScript); err != nil {
			return err
		}
		emit("starting_magpie", runtime.ID, "")
		if err = setup.Exec(ctx, connection, setup.MagpieServiceScript); err != nil {
			return err
		}
	}
	if retained && retain[0].Terminal != nil && resident == nil {
		start, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := startResident(start, fmt.Sprintf(`"$HOME/.local/fennec/bin/fennec-terminal" serve --protocol=%d`, terminalstream.ProtocolVersion))
		cancel()
		if err != nil {
			return err
		}
	}
	configureRoute := func(ctx context.Context, route setup.AgentRoute) ([]setup.RouteResult, error) {
		if route.Problem != "" {
			return []setup.RouteResult{{Agent: route.Agent, Model: route.Model, Status: route.Problem}}, nil
		}
		if resident == nil || !resident.Supports("reconcile_routes") {
			return setup.ReconcileRoutes(ctx, connection, []setup.AgentRoute{route})
		}
		raw, err := resident.Operate(ctx, cloudruntime.LocalOperation{Kind: "reconcile_routes", Routes: []setup.AgentRoute{route}})
		if err != nil {
			return nil, err
		} // Never replay an uncertain mutation on another channel.
		return setup.DecodeRouteResults(raw, []setup.AgentRoute{route})
	}
	emit("migrating_credentials", runtime.ID, "")
	accountsDone := launchprofile.Begin("prepare.accounts.sync")
	accounts, err := func() (setup.ModelAccountsResult, error) {
		request := nativeRequest
		var err error
		if magpie {
			request, err = setup.GatherModelAccountsRequest(home, plugins, ids)
			request.NativeFiles = nativeRequest.Files
			request.NativeProblems = nativeRequest.NativeProblems
		}
		if err != nil {
			return setup.ModelAccountsResult{}, err
		}
		request.DeferCatalog = resident != nil && resident.Supports("deferred_catalog") && resident.Supports("model_catalog")
		// Keep credentials within the existing bounded frame size. Large stores
		// select authenticated stdin before any mutation; never retry on error.
		return syncPreparedAccounts(ctx, request, resident != nil && resident.Supports("model_accounts") && resident.Supports("native_accounts"), func(ctx context.Context, operation cloudruntime.LocalOperation) (json.RawMessage, error) {
			return resident.Operate(ctx, operation)
		}, func(ctx context.Context, request setup.ModelAccountsRequest) (setup.ModelAccountsResult, error) {
			request.DeferCatalog = false
			return setup.SyncModelAccountsRequest(ctx, connection, request)
		})
	}()
	accountsDone()
	if err != nil {
		emit("failed", runtime.ID, err.Error())
		return err
	}
	for _, phase := range []string{"credentials", "plugins", "identity", "catalog", "inventory"} {
		elapsed, ok := accounts.Timings[phase]
		if !ok {
			continue
		}
		// Remote durations observed at receipt, not synchronized cross-host spans.
		launchprofile.Record("prepare.accounts.remote."+phase, time.Now(), time.Duration(elapsed*float64(time.Millisecond)))
	}
	if accounts.CatalogAvailable {
		data, _ := json.Marshal(accounts.Models)
		json.NewEncoder(output).Encode(map[string]string{"type": "model_catalog", "models": string(data)})
	}
	results := []setup.RouteResult{}
	json.NewEncoder(output).Encode(map[string]string{"type": "shared_services_ready", "runtime_id": runtime.ID})
	initialResults, _ := json.Marshal(results)
	json.NewEncoder(output).Encode(map[string]string{"type": "route_results", "runtime_id": runtime.ID, "results": string(initialResults)})
	err = prepareAgentsIndependently(ctx, versions, routes,
		func(ctx context.Context, a setup.Agent) error {
			if accounts.AvailableAgents[a.ID] {
				launchprofile.Record("prepare.binary_reused."+a.ID, time.Now(), 0)
				return nil
			}
			defer launchprofile.Begin("prepare.binary_ensure." + a.ID)()
			script, err := setup.EnsureAgentScript(a)
			if err != nil {
				return err
			}
			return setup.Exec(ctx, connection, script)
		},
		func(ctx context.Context, route setup.AgentRoute) ([]setup.RouteResult, error) {
			defer launchprofile.Begin("prepare.route." + route.Agent)()
			if !magpie {
				status := "credentials_required"
				if nativeRequest.NativeProblems[route.Agent] != "" {
					status = nativeRequest.NativeProblems[route.Agent]
				}
				if accounts.NativeReady[route.Agent] {
					status = "configured"
				}
				return []setup.RouteResult{{Agent: route.Agent, Status: status}}, nil
			}
			return configureRoute(ctx, route)
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

	if magpie {
		results, err = recoverRoutes(ctx, routes, results, 30*time.Second,
			func(ctx context.Context, route setup.AgentRoute) ([]setup.RouteResult, error) {
				return configureRoute(ctx, route)
			}, func(current []setup.RouteResult) {
				encoded, _ := json.Marshal(current)
				json.NewEncoder(output).Encode(map[string]string{"type": "route_results", "runtime_id": runtime.ID, "results": string(encoded)})
			})
		if err != nil {
			return err
		}
	}
	status := "routes_configured_inference_pending"
	for _, result := range results {
		if result.Status != "configured" {
			status = "model_routes_need_attention"
			break
		}
	}
	if !magpie {
		status = "ready"
		for _, result := range results {
			if !stablePreparedRoute(result.Status) && result.Status != "credentials_unavailable" {
				status = "agents_need_attention"
				break
			}
		}
	}
	// All agent results have been published. Optional model-list refresh must
	// not precede verified route readiness or delay the first usable terminal.
	if accounts.CatalogDeferred && resident != nil && resident.Supports("model_catalog") {
		catalogCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		raw, err := resident.Operate(catalogCtx, cloudruntime.LocalOperation{Kind: "model_catalog"})
		cancel()
		var models []string
		if err == nil && json.Unmarshal(raw, &models) == nil {
			data, _ := json.Marshal(models)
			json.NewEncoder(output).Encode(map[string]string{"type": "model_catalog", "models": string(data)})
		}
	}
	cacheable := fingerprintErr == nil && len(results) > 0
	for _, result := range results {
		if !stablePreparedRoute(result.Status) {
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

// Missing or non-portable local credentials are stable user configuration,
// not a reason to repeat cloud installation. Source changes are fingerprinted
// before cache lookup; completed cloud logins invalidate the checkpoint.
func stablePreparedRoute(status string) bool {
	switch status {
	case "configured", "model_selection_required", "local_gateway_not_found", "unsupported_agent",
		"credentials_required", "local_provider_unavailable", "local_credential_command":
		return true
	}
	return false
}
