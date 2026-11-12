package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/launcher"
	"fennec/internal/runta"
	"fennec/internal/sessionid"
	"fennec/internal/setup"
	"path/filepath"
)

func (s *daemonServices) handlers(controller *controlplane.Controller) map[string]control.Handler {
	s.mu.Lock()
	s.loginObserver = func(ctx context.Context, runtime string, data []byte) {
		var listing struct {
			Accounts map[string]string `json:"native_accounts"`
		}
		if json.Unmarshal(data, &listing) == nil && len(listing.Accounts) > 0 {
			_ = s.acceptNativeAccounts(ctx, runtime, listing.Accounts, controller)
		}
	}
	s.mu.Unlock()
	return map[string]control.Handler{
		"runtime.metrics": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return s.runtimeMetrics(ctx, raw, controller)
		},
		"agent.account.get": s.nativeAccount,
		"agent.login.start": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return s.nativeLogin(ctx, raw, "start", controller)
		},
		"agent.login.finish": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return s.nativeLogin(ctx, raw, "finish", controller)
		},
		"agent.login.cancel": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return s.nativeLogin(ctx, raw, "cancel", controller)
		},
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
			if desktop, handled, err := s.setCodexDesktop(raw); handled {
				return desktop, err
			}
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
		"model.get":       s.getNativeModels,
		"model.local.get": s.localModel,
		"model.set": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var scope struct {
				Location string `json:"location"`
			}
			if json.Unmarshal(raw, &scope) != nil || (scope.Location != "" && scope.Location != "cloud" && scope.Location != "local") {
				return nil, &control.Fault{Code: "invalid_params", Message: "Choose a run location"}
			}
			if !newDesktopSettings(s.home).value.Magpie {
				if scope.Location == "local" {
					return nil, &control.Fault{Code: "unsupported_operation", Message: "Choose the local model in your agent"}
				}
				return s.setNativeModel(ctx, raw)
			}
			var choice struct {
				Agent string `json:"agent"`
				Model string `json:"model"`
			}
			if json.Unmarshal(raw, &choice) != nil {
				return nil, &control.Fault{Code: "invalid_params", Message: "Agent and model required"}
			}
			if scope.Location == "local" {
				if err := setup.SetLocalMagpieModel(ctx, s.home, choice.Agent, choice.Model); err != nil {
					return nil, err
				}
				return map[string]any{"saved": true, "agent": choice.Agent, "model": choice.Model, "location": "local"}, nil
			}
			return s.setCloudMagpieModel(ctx, controller, choice.Agent, choice.Model)
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
	err := writePublicJSON(runta.TakeoverPreferencesPath(s.home), map[string]any{"runtime_id": s.config.RuntimeID, "agents": s.config.Selected, "roots": s.config.Roots})
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
