package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/agent"
	r "fennec/internal/runtime"
	"fennec/internal/setup"
	"io"
	"path/filepath"
	"sync"
)

func (s *daemonServices) reconfigureExisting(ctx context.Context, epoch uint64, output io.Writer) (bool, error) {
	s.mu.Lock()
	channel, user, runtimeID, current := s.channel, s.user, s.config.RuntimeID, s.epoch
	s.mu.Unlock()
	if channel == nil || user == "" || runtimeID == "" || current != epoch {
		return false, nil
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
	magpie := newDesktopSettings(s.home).value.Magpie
	ids := agent.CloudAgentIDs()
	sourceIDs := ids
	if !magpie {
		sourceIDs = nil
		cloud := setup.NativeCloudPreferences(filepath.Join(s.home, ".config/fennec"), user, runtimeID)
		for _, id := range ids {
			if !cloud[id] {
				sourceIDs = append(sourceIDs, id)
			}
		}
	}
	native, err := setup.GatherNativeAccounts(ctx, s.home, sourceIDs, magpie)
	if err != nil {
		return true, err
	}
	native.Agents = ids
	request := setup.RuntimeConfiguration{Accounts: native}
	if magpie {
		routes := make([]setup.AgentRoute, len(ids))
		var workers sync.WaitGroup
		for i, id := range ids {
			workers.Add(1)
			go func(i int, id string) {
				defer workers.Done()
				routes[i] = setup.LocalAgentRoutes(ctx, []setup.Agent{{ID: id, Version: "latest"}})[0]
			}(i, id)
		}
		workers.Wait()
		routes, err = setup.ApplyModelPreferences(filepath.Join(s.home, ".config/fennec"), user, routes)
		if err != nil {
			return true, err
		}
		plugins, err := setup.DesiredModelPlugins(s.home)
		if err != nil {
			return true, err
		}
		request.Accounts, err = setup.GatherModelAccountsRequest(s.home, plugins, ids)
		if err != nil {
			return true, err
		}
		request.Accounts.NativeFiles = native.Files
		request.Accounts.NativeProblems = native.NativeProblems
		request.Routes = routes
	}
	data, err := transport.Operate(ctx, r.LocalOperation{Kind: "reconfigure_runtime", Configuration: &request})
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
		if err = emit(map[string]string{"type": "model_catalog", "models": string(models), "runtime_id": runtimeID}); err != nil {
			return true, err
		}
	}
	routes, _ := json.Marshal(result.Routes)
	if err = emit(map[string]string{"type": "route_results", "results": string(routes), "runtime_id": runtimeID}); err != nil {
		return true, err
	}
	return true, emit(map[string]string{"type": "runtime_progress", "stage": "agents_installed", "status": "ready", "runtime_id": runtimeID})
}
