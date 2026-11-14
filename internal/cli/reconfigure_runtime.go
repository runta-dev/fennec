package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/agent"
	"fennec/internal/agentcmd"
	"fennec/internal/cloudstate"
	r "fennec/internal/runtime"
	"fennec/internal/setup"
	"io"
	"path/filepath"
	"sync"
	"time"
)

func (s *daemonServices) reconfigureExisting(ctx context.Context, epoch uint64, output io.Writer) (bool, error) {
	timings := map[string]float64{}
	phase := time.Now()
	mark := func(name string) {
		now := time.Now()
		timings[name] = float64(now.Sub(phase).Microseconds()) / 1000
		phase = now
	}
	s.mu.Lock()
	channel, user, runtimeID, current := s.channel, s.user, s.config.RuntimeID, s.epoch
	s.mu.Unlock()
	if channel == nil || user == "" || runtimeID == "" || current != epoch {
		return false, nil
	}
	if err := cloudstate.New(s.home).Check(user); err != nil {
		return true, err
	}
	transport, err := channel.get(ctx)
	if err != nil {
		return true, err
	}
	if err = transport.Ready(ctx); err != nil {
		return true, err
	}
	if !transport.Supports("reconfigure_runtime") {
		return false, nil
	}
	mark("transport_ready")
	magpie := newDesktopSettings(s.home).value.Magpie
	ids := agent.CloudAgentIDs()
	sourceIDs := ids
	{
		sourceIDs = nil
		cloud := setup.NativeCloudPreferences(filepath.Join(s.home, ".config/fennec"), user, runtimeID)
		for _, id := range ids {
			if !cloud[id] || (magpie && !agentcmd.NativeOnly(id)) {
				sourceIDs = append(sourceIDs, id)
			}
		}
	}
	prepareStarted := time.Now()
	var native setup.ModelAccountsRequest
	var nativeElapsed float64
	request := setup.RuntimeConfiguration{}
	jobs := []func(context.Context) error{func(ctx context.Context) error {
		started := time.Now()
		var err error
		native, err = setup.GatherNativeAccounts(ctx, s.home, sourceIDs, magpie)
		nativeElapsed = float64(time.Since(started).Microseconds()) / 1000
		return err
	}}
	if magpie {
		jobs = append(jobs, func(ctx context.Context) error {
			routes := []setup.AgentRoute{}
			for _, id := range ids {
				if !agentcmd.NativeOnly(id) {
					routes = append(routes, setup.AgentRoute{Agent: id})
				}
			}
			routes, err := setup.ApplyModelPreferences(filepath.Join(s.home, ".config/fennec"), user, routes)
			if err != nil {
				mark("local_routes")
				return err
			}
			var workers sync.WaitGroup
			for i, route := range routes {
				if route.Model != "" {
					continue
				}
				workers.Add(1)
				go func(i int, id string) {
					defer workers.Done()
					routes[i] = setup.LocalAgentRoutes(ctx, []setup.Agent{{ID: id, Version: "latest"}})[0]
				}(i, route.Agent)
			}
			workers.Wait()
			mark("local_routes")
			plugins, err := setup.DesiredModelPlugins(s.home)
			mark("plugins")
			if err != nil {
				return err
			}
			request.Accounts, err = setup.GatherModelAccountsRequest(s.home, plugins, ids)
			mark("gateway_accounts")
			if err != nil {
				return err
			}
			request.Routes = routes
			return nil
		})
	}
	if err := gatherConfigurationInputs(ctx, jobs...); err != nil {
		return true, err
	}
	timings["native_accounts"] = nativeElapsed
	timings["local_prepare_wall"] = float64(time.Since(prepareStarted).Microseconds()) / 1000
	phase = time.Now()
	native.Agents = ids
	if magpie {
		request.Accounts.NativeFiles = native.Files
		request.Accounts.NativeProblems = native.NativeProblems
	} else {
		request.Accounts = native
	}
	data, wireTimings, err := transport.OperateMeasured(ctx, r.LocalOperation{Kind: "reconfigure_runtime", Configuration: &request})
	mark("remote_round_trip")
	for name, value := range wireTimings {
		timings["transport_"+name] = value
	}
	if err != nil {
		return true, err
	}
	result, err := setup.DecodeRuntimeConfigurationResult(data, request)
	if err != nil {
		return true, err
	}
	if err = ctx.Err(); err != nil {
		return true, err
	}
	if result.NeedsSetup {
		return false, nil
	}
	s.mu.Lock()
	valid := s.epoch == epoch && s.config.RuntimeID == runtimeID
	s.mu.Unlock()
	if !valid {
		return true, context.Canceled
	}
	// A newer user choice is already queued by the controller. Do not publish
	// readiness for the superseded mode while that choice is being applied.
	if newDesktopSettings(s.home).value.Magpie != magpie {
		return true, nil
	}
	emit := func(event map[string]string) error {
		if err := json.NewEncoder(output).Encode(event); err != nil {
			return err
		}
		raw, _ := json.Marshal(event)
		var decoded map[string]json.RawMessage
		_ = json.Unmarshal(raw, &decoded)
		s.runtimeEvent(epoch, decoded)
		return nil
	}
	if magpie {
		models, _ := json.Marshal(result.Models)
		catCtx, catCancel := context.WithTimeout(ctx, 2*time.Second)
		catalog, _ := setup.LocalGatewayCatalog(catCtx, s.home)
		catCancel()
		catJSON, _ := json.Marshal(catalog)
		if err = emit(map[string]string{"type": "model_catalog", "models": string(models), "catalog": string(catJSON), "runtime_id": runtimeID}); err != nil {
			return true, err
		}
	}
	routes, _ := json.Marshal(result.Routes)
	if err = emit(map[string]string{"type": "route_results", "results": string(routes), "runtime_id": runtimeID}); err != nil {
		return true, err
	}
	mark("publish")
	profile, _ := json.Marshal(map[string]any{"local_ms": timings, "remote_ms": result.Timings})
	if err = emit(map[string]string{"type": "configuration_profile", "timings": string(profile), "runtime_id": runtimeID}); err != nil {
		return true, err
	}
	return true, emit(map[string]string{"type": "runtime_progress", "stage": "agents_installed", "status": "ready", "runtime_id": runtimeID})
}
