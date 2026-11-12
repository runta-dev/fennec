package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	r "fennec/internal/runtime"
	"fennec/internal/setup"
	"os"
	"path/filepath"
	"time"
)

// setCloudMagpieModel switches a single cloud agent's Magpie model directly
// over the warm resident channel (~180ms end-to-end) instead of running a full
// multi-second runtime preparation/reconfiguration cycle. Falls back to
// controller.Reconcile only when the resident channel is not yet established.
func (s *daemonServices) setCloudMagpieModel(ctx context.Context, controller *controlplane.Controller, agent, model string) (any, error) {
	lifecycle, _, epoch, ok := s.account.Session()
	if !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in first"}
	}
	s.mu.Lock()
	if s.epoch != epoch || s.user == "" {
		s.mu.Unlock()
		return nil, &control.Fault{Code: "not_ready", Message: "Account services are not ready"}
	}
	if err := setup.SaveModelPreference(filepath.Join(s.home, ".config/fennec"), s.user, agent, model); err != nil {
		s.mu.Unlock()
		return nil, &control.Fault{Code: "invalid_model", Message: "A valid agent and provider/model are required"}
	}
	channel := s.channel
	runtimeID := s.config.RuntimeID
	s.mu.Unlock()

	if channel != nil && runtimeID != "" {
		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		stop := context.AfterFunc(lifecycle, cancel)
		transport := channel.peekAlive()
		if transport == nil {
			transport, _ = channel.get(opCtx)
		}
		if transport != nil && transport.Supports("reconcile_routes") {
			rttStart := time.Now()
			desired := []setup.AgentRoute{{Agent: agent, Model: model}}
			raw, opErr := transport.Operate(opCtx, r.LocalOperation{Kind: "reconcile_routes", Routes: desired})
			rttMS := float64(time.Since(rttStart).Microseconds()) / 1000
			stop()
			cancel()
			if opErr != nil {
				return nil, opErr
			}
			decoded, decErr := setup.DecodeRouteResults(raw, desired)
			if decErr != nil || len(decoded) != 1 {
				return nil, &control.Fault{Code: "verification_failed", Message: "Model change was not confirmed"}
			}
			updated := decoded[0]
			merged := s.mergeAndPublishRouteResult(epoch, runtimeID, controller, updated)
			if updated.Status != "configured" {
				return nil, &control.Fault{Code: updated.Status, Message: "Selected model is unavailable"}
			}
			return map[string]any{
				"saved":      true,
				"agent":      agent,
				"model":      model,
				"location":   "cloud",
				"status":     updated.Status,
				"routes":     merged,
				"timings_ms": map[string]float64{"remote_round_trip": rttMS},
			}, nil
		}
		stop()
		cancel()
	}

	s.mu.Lock()
	if s.epoch == epoch {
		delete(s.ready, agent)
		s.reconcileSelectionLocked()
	}
	s.mu.Unlock()
	if controller == nil {
		return map[string]any{"saved": true, "agent": agent, "model": model, "location": "cloud"}, nil
	}
	state, err := controller.Reconcile(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"saved": true, "agent": agent, "model": model, "location": "cloud", "preparation": state}, nil
}

func (s *daemonServices) mergeAndPublishRouteResult(epoch uint64, runtimeID string, controller *controlplane.Controller, updated setup.RouteResult) []setup.RouteResult {
	var existing []setup.RouteResult
	if raw, err := os.ReadFile(filepath.Join(s.home, ".config/fennec/route-results.json")); err == nil {
		_ = json.Unmarshal(raw, &existing)
	}
	found := false
	for i := range existing {
		if existing[i].Agent == updated.Agent {
			existing[i] = updated
			found = true
			break
		}
	}
	if !found {
		existing = append(existing, updated)
	}
	encoded, _ := json.Marshal(existing)
	eventText := map[string]string{
		"type":       "route_results",
		"results":    string(encoded),
		"runtime_id": runtimeID,
	}
	rawEvent, _ := json.Marshal(eventText)
	var decodedEvent map[string]json.RawMessage
	_ = json.Unmarshal(rawEvent, &decodedEvent)
	s.runtimeEvent(epoch, decodedEvent)
	s.mu.Lock()
	if s.epoch == epoch {
		s.reconcileSelectionLocked()
	}
	s.mu.Unlock()
	if controller != nil {
		controller.RecordEvent(epoch, eventText)
	}
	return existing
}
