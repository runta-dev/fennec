package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/agent"
	"fennec/internal/launchprofile"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"fennec/internal/terminalstream"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// prepareRuntime is shared by the standalone daemon and the legacy command.
// Its cancellation belongs to the caller's account lifecycle.
type preparationRetainer struct {
	Runtime    func(resolvedComputer) error
	Connection func(string, *ssh.Client) bool
	Terminal   func(string, *terminalstream.Transport) bool
}

func prepareRuntime(ctx context.Context, computer cloudComputer, output io.Writer, retain ...preparationRetainer) error {
	defer launchprofile.Begin("prepare.total")()
	ctx, cancelPreparation := context.WithCancel(ctx)
	defer cancelPreparation()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	artifacts := filepath.Join(filepath.Dir(executable), "..", "Resources")
	prefs := newDesktopSettings(home).value
	magpie := prefs.Magpie
	go setup.PrewarmNativeEnvironment(ctx)
	discoveredInstalled := map[string]bool{}
	for _, item := range agent.PanelCatalog(agent.Discover()) {
		if item.Cloud && item.Installed {
			discoveredInstalled[item.ID] = true
		}
	}
	versions := []setup.Agent{}
	var ids []string
	for _, id := range agent.CloudAgentIDs() {
		if len(discoveredInstalled) > 0 && !discoveredInstalled[id] && id != "cursor" && id != "grok" && id != "opencode" && id != "agy" {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		ids = agent.CloudAgentIDs()
	}
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
				if prefs.magpieFor(version.ID) {
					localRoutes[i] = setup.ConfiguredAgentRoutes(home, []setup.Agent{version})[0]
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
	scope := computer.Scope()
	emit := func(stage, id, status string) {
		json.NewEncoder(output).Encode(map[string]string{"type": "runtime_progress", "stage": stage, "runtime_id": id, "status": status})
	}
	emit("allocating", "", "")
	var inputs preparationInputs
	inputsReady := make(chan struct{})
	connectDone := launchprofile.Begin("prepare.connect")
	connection, runtime, err := computer.Resolve(ctx, emit, func(resolved resolvedComputer) error {
		if len(retain) == 1 && retain[0].Runtime != nil {
			if err := retain[0].Runtime(resolved); err != nil {
				return err
			}
		}
		go func(owner, id string) {
			defer close(inputsReady)
			select {
			case <-ctx.Done():
				inputs.err = ctx.Err()
				return
			case <-localReady:
			}
			inputs = gatherPreparationInputs(ctx, home, scope, owner, id, artifacts, prefs, ids, versions, localRoutes)
		}(resolved.Owner, resolved.ID)
		return nil
	})
	if err != nil {
		reason := err.Error()
		if strings.Contains(reason, "billing_restricted") {
			reason = "billing_restricted"
		}
		emit("failed", runtime.ID, reason)
		return err
	}
	connectDone()
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
	json.NewEncoder(output).Encode(map[string]string{"type": "connector_ready", "runtime_id": runtime.ID})
	var resident *terminalstream.Transport
	startResident := func(ctx context.Context, command string) error {
		defer launchprofile.Begin("prepare.resident")()
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
	// Resume early only when this build's installation was already confirmed.
	// Protocol compatibility alone cannot prove the running service includes an
	// updated configuration reader or new operation. Starting the old executable
	// before replacing it leaves the broker bound to old code for this lifetime.
	// Unchanged daemon restarts still resume before account synchronization.
	installation, installationErr := installationFingerprint(home, scope, runtime.ID, versions, artifacts)
	residentChecked := false
	residentFingerprint := ""
	if retained && retain[0].Terminal != nil && reusableEarlyResident(home, installation, installationErr) {
		command := fmt.Sprintf(`"$HOME/.local/fennec/bin/fennec-terminal" serve --protocol=%d`, terminalstream.ProtocolVersion)
		checked := false
		select {
		case <-inputsReady:
			if inputs.err == nil && inputs.fingerprintErr == nil {
				if _, ok := readPreparationCheckpoint(home, inputs.fingerprint); ok {
					command = checkpointTerminalCommandForMarker(inputs.fingerprint, ids, "prepared-runtime", magpie)
					checked = true
				}
			}
		default:
		}
		// Local credential gathering can finish just after this point. Check the
		// last complete remote snapshot in the same exec as resident startup,
		// then reuse that proof only if the fresh inputs match it below.
		if !checked {
			if previous, ok := loadPreparationCheckpoint(home); ok {
				command = checkpointTerminalCommandForMarker(previous.Fingerprint, ids, "prepared-runtime", magpie)
				residentFingerprint = previous.Fingerprint
				checked = true
			}
		} else {
			residentFingerprint = inputs.fingerprint
		}
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := startResident(check, command)
		cancel()
		residentChecked = err == nil && checked
		if err != nil && checked {
			check, cancel = context.WithTimeout(ctx, 5*time.Second)
			err = startResident(check, fmt.Sprintf(`"$HOME/.local/fennec/bin/fennec-terminal" serve --protocol=%d`, terminalstream.ProtocolVersion))
			cancel()
		}
		if err == nil {
			json.NewEncoder(output).Encode(map[string]string{"type": "shared_services_ready", "runtime_id": runtime.ID})
		}
	}
	select {
	case <-inputsReady:
	case <-ctx.Done():
		return ctx.Err()
	}
	if inputs.err != nil {
		return inputs.err
	}
	nativeRequest, routes, plugins := inputs.native, inputs.routes, inputs.plugins
	gatewayFingerprint := gatewayPreparationFingerprint(scope, runtime.ID)
	fingerprint, fingerprintErr := inputs.fingerprint, inputs.fingerprintErr
	cacheStatus := func(result string) {
		data, _ := json.Marshal(map[string]any{"result": result, "changed_inputs": inputs.changedInputs, "previous_inputs_known": inputs.hasPreviousInputs})
		_ = os.WriteFile(filepath.Join(home, ".config/fennec/preparation-cache-status.json"), data, 0600)
	}
	cacheStatus("inputs_changed_or_no_checkpoint")
	healthCheck := func(ctx context.Context, fingerprint string, agents []string, marker string, magpie bool) error {
		var transport preparationHealthTransport
		if resident != nil {
			transport = resident
		}
		return checkPreparedRuntime(ctx, transport, setup.PreparationHealth{Fingerprint: fingerprint, Agents: agents, Marker: marker, Magpie: magpie}, func(ctx context.Context, script string) error {
			return setup.ExecPublicCheck(ctx, connection, script)
		})
	}
	publishCheckpoint := func(checkpoint preparationCheckpoint) error {
		script := strings.ReplaceAll(installationMarkerScript(checkpoint.Fingerprint), "prepared-installation", "prepared-runtime")
		if err := setup.ExecPublicCheck(ctx, connection, script); err != nil {
			return err
		}
		return savePreparationCheckpoint(home, checkpoint)
	}
	if fingerprintErr == nil {
		if checkpoint, ok := readPreparationCheckpoint(home, fingerprint); ok {
			check, cancel := context.WithTimeout(ctx, 5*time.Second)
			healthDone := launchprofile.Begin("prepare.health")
			var err error
			if resident == nil && retained && retain[0].Terminal != nil {
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
			} else if !residentChecked || residentFingerprint != fingerprint {
				err = healthCheck(check, fingerprint, ids, "prepared-runtime", magpie)
			}
			healthDone()
			cancel()
			if err == nil {
				cacheStatus("hit")
				// Migrate an older checkpoint only after its complete fingerprint
				// and live remote health have both been verified.
				if checkpoint.ConfigurationFingerprint == "" || (magpie && checkpoint.GatewayFingerprint == "") {
					checkpoint.ConfigurationFingerprint = inputs.configurationFingerprint
					if magpie {
						checkpoint.GatewayFingerprint = gatewayFingerprint
					}
					_ = savePreparationCheckpoint(home, checkpoint)
				}
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
	installationReady := false
	if installationErr == nil && installationCheckpointMatches(home, installation) {
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		if resident == nil && retained && retain[0].Terminal != nil {
			installationReady = startResident(check, checkpointTerminalCommandForMarker(installation, nil, "prepared-installation", false)) == nil
		} else {
			installationReady = healthCheck(check, installation, nil, "prepared-installation", false) == nil
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
	// Fast ~5s onboarding critical path: verify guest capacity, start Herdr session
	// daemon, and upload static Go fennec-terminal concurrently. Heavy Node.js +
	// npm global agent CLI installations are offloaded after the UI workspace unlocks!
	if !installationReady {
		emit("installing_session_service", runtime.ID, "")
		installDone := launchprofile.Begin("prepare.install")
		type setupStep struct {
			reason string
			stage  string
			run    func(context.Context) error
		}
		coreSteps := []setupStep{
			{"cloud resource allocation has not taken effect", "capacity", func(stepCtx context.Context) error {
				return setup.Exec(stepCtx, connection, setup.GuestCapacityScript)
			}},
			{"agent version manifest sync failed", "manifest", func(stepCtx context.Context) error {
				return setup.SyncAgentManifest(stepCtx, connection, versions)
			}},
			{"session service reconciliation failed", "herdr", func(stepCtx context.Context) error {
				return setup.Exec(stepCtx, connection, setup.HerdrSetupScript)
			}},
			{"Go terminal service installation failed", "terminal_binary", func(stepCtx context.Context) error {
				return setup.InstallTerminalBinary(stepCtx, connection, filepath.Join(filepath.Dir(executable), "..", "Resources"))
			}},
		}
		stepCtx, cancelSteps := context.WithCancel(ctx)
		var stepWG sync.WaitGroup
		var stepMu sync.Mutex
		var firstErr error
		var firstReason string
		for _, step := range coreSteps {
			stepWG.Add(1)
			go func(st setupStep) {
				defer stepWG.Done()
				defer launchprofile.Begin("prepare.install." + st.stage)()
				if runErr := st.run(stepCtx); runErr != nil {
					stepMu.Lock()
					if firstErr == nil {
						firstErr = runErr
						firstReason = st.reason
						cancelSteps()
					}
					stepMu.Unlock()
				}
			}(step)
		}
		stepWG.Wait()
		installDone()
		cancelSteps()
		if firstErr != nil {
			emit("failed", runtime.ID, firstReason)
			return firstErr
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
	if resident != nil {
		// Ownership, host key, protocol, and service readiness are verified.
		// Existing sessions retain their own configuration and may reattach now;
		// new launches remain gated by the route results published below.
		json.NewEncoder(output).Encode(map[string]string{"type": "shared_services_ready", "runtime_id": runtime.ID})
	}
	if fingerprintErr == nil && installationErr == nil {
		// An executable update does not invalidate already-applied credentials.
		// Core installation above still runs; independently check the previous
		// configuration marker, all agent binaries, and the gateway before reuse.
		checkpoint, ok := reusableConfigurationCheckpoint(ctx, home, inputs.configurationFingerprint, func(ctx context.Context, previous string) error {
			return healthCheck(ctx, previous, ids, "prepared-runtime", magpie)
		})
		if ok {
			checkpoint.Fingerprint = fingerprint
			if magpie {
				checkpoint.GatewayFingerprint = gatewayFingerprint
			}
			if err := publishCheckpoint(checkpoint); err != nil {
				return err
			}
			if setup.ExecPublicCheck(ctx, connection, installationMarkerScript(installation)) == nil {
				_ = writePublicJSON(installationCheckpointPath(home), map[string]string{"fingerprint": installation})
			}
			cacheStatus("configuration_reused_after_installation")
			for _, event := range checkpoint.Events {
				json.NewEncoder(output).Encode(event)
			}
			return nil
		}
	}
	if magpie {
		gatewayReady := reusableGatewayCheckpoint(ctx, home, gatewayFingerprint, func(ctx context.Context, marker string) error {
			return healthCheck(ctx, marker, nil, "prepared-runtime", true)
		})
		if !gatewayReady {
			emit("installing_magpie", runtime.ID, "")
			if err = setup.Exec(ctx, connection, setup.MagpieSetupScript); err != nil {
				return err
			}
			emit("starting_magpie", runtime.ID, "")
			if err = setup.Exec(ctx, connection, setup.MagpieServiceScript); err != nil {
				return err
			}
		} else {
			launchprofile.Record("prepare.gateway_reused", time.Now(), 0)
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
			request.NativeAgents = prefs.nativeAgents(ids)
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
	emit("configuring_routes", runtime.ID, "")
	// A credential changed on both computers that neither side could confirm
	// usable stays as it is on each; the next sync decides. Diagnostics only.
	for _, name := range accounts.NativeDeferred {
		agent, _, _ := strings.Cut(name, "/")
		launchprofile.Record("prepare.accounts.credential_deferred."+agent, time.Now(), 0)
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
		catCtx, catCancel := context.WithTimeout(ctx, 2*time.Second)
		catalog, _ := setup.LocalGatewayCatalog(catCtx, home)
		catCancel()
		catJSON, _ := json.Marshal(catalog)
		json.NewEncoder(output).Encode(map[string]string{"type": "model_catalog", "models": string(data), "catalog": string(catJSON)})
	}
	results := []setup.RouteResult{}
	json.NewEncoder(output).Encode(map[string]string{"type": "shared_services_ready", "runtime_id": runtime.ID})
	if !magpie {
		for _, route := range routes {
			st := "credentials_required"
			if nativeRequest.NativeProblems[route.Agent] != "" {
				st = nativeRequest.NativeProblems[route.Agent]
			}
			if accounts.NativeReady[route.Agent] {
				st = "configured"
			}
			// Authentication alone does not prove the agent binary is present.
			// Publish success early only for the verified existing inventory.
			if st == "configured" && !accounts.AvailableAgents[route.Agent] {
				continue
			}
			results = append(results, setup.RouteResult{Agent: route.Agent, Status: st})
		}
	}
	initialResults, _ := json.Marshal(results)
	json.NewEncoder(output).Encode(map[string]string{"type": "route_results", "runtime_id": runtime.ID, "results": string(initialResults)})
	// Unlock the main workspace UI right away (~5s end-to-end onboarding!) while
	// Node.js and missing agent CLIs finish warming up asynchronously below.
	if !magpie {
		earlyStatus := "ready"
		for _, result := range results {
			if !stablePreparedRoute(result.Status) && result.Status != "credentials_unavailable" {
				earlyStatus = "agents_need_attention"
				break
			}
		}
		emit("agents_installed", runtime.ID, earlyStatus)
	}
	if !installationReady {
		nodeDone := launchprofile.Begin("prepare.install.node")
		err = setup.Exec(ctx, connection, setup.NodeSetupScript)
		nodeDone()
		if err != nil {
			emit("failed", runtime.ID, "Node setup failed")
			return err
		}
		if installationErr == nil && setup.ExecPublicCheck(ctx, connection, installationMarkerScript(installation)) == nil {
			_ = writePublicJSON(installationCheckpointPath(home), map[string]string{"fingerprint": installation})
		}
	}
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
			if !prefs.magpieFor(route.Agent) {
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
			results = updatePreparedRoutes(results, one)
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
			catCtx, catCancel := context.WithTimeout(ctx, 2*time.Second)
			catalog, _ := setup.LocalGatewayCatalog(catCtx, home)
			catCancel()
			catJSON, _ := json.Marshal(catalog)
			json.NewEncoder(output).Encode(map[string]string{"type": "model_catalog", "models": string(data), "catalog": string(catJSON)})
		}
	}
	cacheable := fingerprintErr == nil && len(results) > 0
	for _, result := range results {
		if !stablePreparedRoute(result.Status) {
			cacheable = false
		}
	}
	if cacheable {
		checkpoint := preparationCheckpoint{Fingerprint: fingerprint, ConfigurationFingerprint: inputs.configurationFingerprint}
		if magpie {
			checkpoint.GatewayFingerprint = gatewayFingerprint
		}
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
		_ = publishCheckpoint(checkpoint)
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
