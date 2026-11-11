package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/hookbridge"
	"fennec/internal/hookplugins"
	"fennec/internal/sessionid"
	"fmt"
)

type hookLease struct {
	epoch                   uint64
	runtime, session, owner string
	executors               map[string]hookbridge.Executor
	lifetime                context.Context
	cancel                  context.CancelFunc
}

func (s *daemonServices) hookHandlers() map[string]control.Handler {
	return map[string]control.Handler{
		"hooks.config.get": func(context.Context, json.RawMessage) (any, error) { return hookbridge.LoadConfig(s.home) },
		"hooks.config.set": func(_ context.Context, raw json.RawMessage) (any, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			cfg, err := hookbridge.ParseConfig(raw)
			if err != nil {
				return nil, err
			}
			if err = hookbridge.SaveConfig(s.home, cfg); err != nil {
				return nil, err
			}
			return cfg, nil
		},
		"hooks.config.remove": func(_ context.Context, raw json.RawMessage) (any, error) {
			var request struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &request) != nil || request.Name == "" {
				return nil, fmt.Errorf("hook name required")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			cfg, err := hookbridge.LoadConfig(s.home)
			if err != nil {
				return nil, err
			}
			var keep []hookbridge.Definition
			for _, d := range cfg.Hooks {
				if d.Name != request.Name {
					keep = append(keep, d)
				}
			}
			cfg.Hooks = keep
			if err = hookbridge.SaveConfig(s.home, cfg); err != nil {
				return nil, err
			}
			return cfg, nil
		},
		"hooks.bind": s.bindHooks, "hooks.execute": s.executeHook,
		"hooks.unbind": func(_ context.Context, raw json.RawMessage) (any, error) {
			var request struct {
				LeaseID string `json:"lease_id"`
			}
			if json.Unmarshal(raw, &request) != nil {
				return nil, fmt.Errorf("invalid hook lease")
			}
			s.mu.Lock()
			if lease, ok := s.hookLeases[request.LeaseID]; ok && lease.cancel != nil {
				lease.cancel()
			}
			delete(s.hookLeases, request.LeaseID)
			s.mu.Unlock()
			return map[string]bool{"removed": true}, nil
		},
	}
}
func (s *daemonServices) bindHooks(ctx context.Context, raw json.RawMessage) (any, error) {
	var request struct {
		RuntimeID string              `json:"runtime_id"`
		SessionID string              `json:"session_id"`
		Context   hookplugins.Context `json:"context"`
	}
	if len(raw) > 256<<10 || json.Unmarshal(raw, &request) != nil || !sessionid.Valid(request.SessionID) {
		return nil, fmt.Errorf("invalid hook registration")
	}
	lifecycle, _, epoch, ok := s.account.Session()
	if !ok || lifecycle.Err() != nil {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in before authorizing local hooks"}
	}
	catalog, err := hookplugins.Catalog(s.home, request.Context)
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(nonce[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.epoch != epoch || s.user == "" || request.RuntimeID != s.config.RuntimeID || s.ctx == nil || s.ctx.Err() != nil {
		return nil, fmt.Errorf("hook runtime identity is not ready")
	}
	if s.hookLeases == nil {
		s.hookLeases = map[string]hookLease{}
	}
	if len(s.hookLeases) >= 256 {
		return nil, fmt.Errorf("too many local hook clients")
	}
	lifetime, cancel := context.WithCancel(lifecycle)
	s.hookLeases[id] = hookLease{epoch: epoch, owner: s.user, runtime: request.RuntimeID, session: request.SessionID, executors: catalog, lifetime: lifetime, cancel: cancel}
	return map[string]string{"lease_id": id}, nil
}
func (s *daemonServices) executeHook(ctx context.Context, raw json.RawMessage) (any, error) {
	var request hookbridge.Call
	if len(raw) > 2*hookbridge.PayloadLimit+2048 || json.Unmarshal(raw, &request) != nil || len(request.Input) > hookbridge.PayloadLimit {
		return hookbridge.Unregistered(), nil
	}
	lifecycle, _, epoch, ok := s.account.Session()
	if !ok {
		return hookbridge.Unregistered(), nil
	}
	s.mu.Lock()
	lease, exists := s.hookLeases[request.LeaseID]
	permissions := s.permissions
	current := s.ctx
	valid := exists && s.epoch == epoch && lease.epoch == epoch && lease.owner == s.user && lease.runtime == s.config.RuntimeID && lease.runtime == request.RuntimeID && lease.session == request.SessionID
	executor := lease.executors[request.BindingID]
	s.mu.Unlock()
	if !valid || executor == nil || lease.lifetime == nil || lease.lifetime.Err() != nil {
		return hookbridge.Unregistered(), nil
	}
	if current == nil || current.Err() != nil || permissions == nil {
		return hookbridge.Failure(executor.Binding(), "permission service is unavailable"), nil
	}
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	stopLease := context.AfterFunc(lease.lifetime, cancel)
	defer stopLease()
	stop := context.AfterFunc(lifecycle, cancel)
	defer stop()
	stopService := context.AfterFunc(current, cancel)
	defer stopService()
	result := hookbridge.Execute(operation, lease.owner, lease.runtime, executor, request.Input, func(prompt context.Context, description, scope string) (context.Context, func(), error) {
		return permissions.AuthorizeHook(prompt, operation, description, scope)
	})
	return result, nil
}

// Keep the execution RPC longer than ordinary control calls because the user
// may be reading a native permission prompt. Cancellation still closes it.
