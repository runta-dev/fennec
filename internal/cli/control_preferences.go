package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/launcher"
	"fennec/internal/sessionid"
	"fennec/internal/setup"
	"path/filepath"
)

func (s *daemonServices) handlers(controller *controlplane.Controller) map[string]control.Handler {
	return map[string]control.Handler{
		"connection.status": func(context.Context, json.RawMessage) (any, error) {
			s.mu.Lock()
			channel := s.channel
			s.mu.Unlock()
			if channel == nil {
				return map[string]any{"ready": false, "generation": 0}, nil
			}
			channel.mu.Lock()
			defer channel.mu.Unlock()
			return map[string]any{"ready": channel.transport != nil && channel.transport.IsReady(), "generation": channel.generation, "connection_started_unix_ms": channel.shared.StartedUnixMS()}, nil
		},
		"sessions.dispatch": s.dispatchSession,
		"sessions.allocate": func(context.Context, json.RawMessage) (any, error) {
			id, err := sessionid.Allocate(s.home)
			return map[string]string{"session_id": id}, err
		},
		"account.logout": func(ctx context.Context, _ json.RawMessage) (any, error) {
			if err := s.account.Logout(ctx); err != nil {
				return nil, err
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if err := launcher.PublishEnabled(filepath.Join(s.home, ".config/fennec/takeover.json"), nil); err != nil {
				return nil, err
			}
			return s.account.Snapshot(), nil
		},
		"takeover.get": func(context.Context, json.RawMessage) (any, error) {
			return s.publicFields("selected", "active", "broker_ready", "selection_known", "coverage"), nil
		},
		"takeover.set": func(ctx context.Context, raw json.RawMessage) (any, error) {
			result, err := s.setTakeover(ctx, raw)
			if err != nil {
				return nil, err
			}
			var request struct {
				Agent   string
				Enabled bool
			}
			json.Unmarshal(raw, &request)
			s.mu.Lock()
			ready := s.ready[request.Agent]
			s.mu.Unlock()
			if request.Enabled && !ready {
				if _, err := controller.PrepareIfIdle(ctx); err != nil {
					return nil, err
				}
			}
			return result, nil
		},
		"approvals.list": func(context.Context, json.RawMessage) (any, error) {
			return s.publicFields("approvals", "connector"), nil
		},
		"approvals.decide": s.decide,
		"trust.clear": func(ctx context.Context, _ json.RawMessage) (any, error) {
			return s.decide(ctx, json.RawMessage(`{"action":"clear_trust"}`))
		},
		"model.set": func(ctx context.Context, raw json.RawMessage) (any, error) {
			if !newDesktopSettings(s.home).value.Magpie {
				return nil, &control.Fault{Code: "integration_disabled", Message: "Enable Magpie in Settings to switch cloud models"}
			}
			var choice struct {
				Agent string `json:"agent"`
				Model string `json:"model"`
			}
			if json.Unmarshal(raw, &choice) != nil {
				return nil, &control.Fault{Code: "invalid_params", Message: "Agent and model required"}
			}
			_, _, epoch, ok := s.account.Session()
			if !ok {
				return nil, &control.Fault{Code: "login_required", Message: "Sign in first"}
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.epoch != epoch || s.user == "" {
				return nil, &control.Fault{Code: "not_ready", Message: "Account services are not ready"}
			}
			if err := setup.SaveModelPreference(filepath.Join(s.home, ".config/fennec"), s.user, choice.Agent, choice.Model); err != nil {
				return nil, &control.Fault{Code: "invalid_model", Message: "A valid agent and provider/model are required"}
			}
			delete(s.ready, choice.Agent)
			s.reconcileSelectionLocked()
			state, err := controller.Reconcile(ctx)
			if err != nil {
				return nil, err
			}
			return map[string]any{"saved": true, "agent": choice.Agent, "model": choice.Model, "preparation": state}, nil
		},
	}
}
func (s *daemonServices) setTakeover(ctx context.Context, raw json.RawMessage) (any, error) {
	var request struct {
		Agent   string `json:"agent"`
		Enabled *bool  `json:"enabled"`
	}
	if json.Unmarshal(raw, &request) != nil || !launcher.Supported[request.Agent] || request.Enabled == nil {
		return nil, &control.Fault{Code: "invalid_params", Message: "Supported agent and explicit enabled value required"}
	}
	lifecycle, _, epoch, ok := s.account.Session()
	if !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in first"}
	}
	if lifecycle.Err() != nil {
		return nil, lifecycle.Err()
	}
	s.mu.Lock()
	if s.epoch != epoch || s.config.RuntimeID == "" {
		s.mu.Unlock()
		return nil, &control.Fault{Code: "not_ready", Message: "Prepare the shared runtime first"}
	}
	before := s.selected[request.Agent]
	if *request.Enabled {
		s.selected[request.Agent] = true
	} else {
		delete(s.selected, request.Agent)
	}
	s.reconcileSelectionLocked()
	err := writePublicJSON(filepath.Join(s.home, ".config/fennec/takeover-preferences.json"), map[string]any{"runtime_id": s.config.RuntimeID, "agents": s.config.Selected, "roots": s.config.Roots})
	if err != nil {
		if before {
			s.selected[request.Agent] = true
		} else {
			delete(s.selected, request.Agent)
		}
		s.reconcileSelectionLocked()
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	return s.snapshot(), nil
}

func (s *daemonServices) publicFields(keys ...string) any {
	snapshot, _ := s.snapshot().(map[string]any)
	result := map[string]any{}
	for _, key := range keys {
		if value, ok := snapshot[key]; ok {
			result[key] = value
		}
	}
	return result
}
