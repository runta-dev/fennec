package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	r "fennec/internal/runtime"
	"fennec/internal/setup"
	"time"
)

type cachedNativeAccount struct {
	View setup.NativeAccountView
	At   time.Time
}

func (s *daemonServices) nativeAccount(ctx context.Context, raw json.RawMessage) (any, error) {
	var request struct {
		Agent    string `json:"agent"`
		Location string `json:"location"`
		Refresh  bool   `json:"refresh"`
	}
	if json.Unmarshal(raw, &request) != nil || !map[string]bool{"codex": true, "claude": true, "pi": true}[request.Agent] || (request.Location != "local" && request.Location != "cloud") {
		return nil, &control.Fault{Code: "invalid_params", Message: "Choose an agent and run location"}
	}
	if request.Location == "local" {
		s.nativeAccountMu.Lock()
		if cached, ok := s.nativeAccountCache[request.Agent]; ok && !request.Refresh && time.Since(cached.At) < 30*time.Second {
			s.nativeAccountMu.Unlock()
			return cached.View, nil
		}
		if pending := s.nativeAccountPending[request.Agent]; pending != nil {
			s.nativeAccountMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-pending:
			}
			s.nativeAccountMu.Lock()
			cached := s.nativeAccountCache[request.Agent]
			s.nativeAccountMu.Unlock()
			return cached.View, nil
		}
		if s.nativeAccountCache == nil {
			s.nativeAccountCache = map[string]cachedNativeAccount{}
		}
		if s.nativeAccountPending == nil {
			s.nativeAccountPending = map[string]chan struct{}{}
		}
		pending := make(chan struct{})
		s.nativeAccountPending[request.Agent] = pending
		s.nativeAccountMu.Unlock()
		source, err := setup.GatherNativeAccounts(ctx, s.home, []string{request.Agent})
		view := setup.NativeAccountFromFiles(request.Agent, source.Files)
		view.Location = "local"
		if err != nil || source.NativeProblems[request.Agent] == "credentials_unavailable" {
			view.Status = "unavailable"
		}
		s.nativeAccountMu.Lock()
		s.nativeAccountCache[request.Agent] = cachedNativeAccount{View: view, At: time.Now()}
		delete(s.nativeAccountPending, request.Agent)
		close(pending)
		s.nativeAccountMu.Unlock()
		return view, nil
	}
	lifecycle, _, epoch, ok := s.account.Session()
	if !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in to Runta first"}
	}
	s.mu.Lock()
	channel, current := s.channel, s.epoch
	s.mu.Unlock()
	if channel == nil || current != epoch {
		return nil, &control.Fault{Code: "not_ready", Message: "Cloud computer is connecting", Retryable: true}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(lifecycle, cancel)
	defer stop()
	transport, err := channel.get(ctx)
	if err != nil {
		return nil, err
	}
	if !transport.Supports("native_account") {
		return nil, &control.Fault{Code: "not_ready", Message: "Cloud computer is updating", Retryable: true}
	}
	data, err := transport.Operate(ctx, r.LocalOperation{Kind: "native_account", NativeModel: &setup.NativeModelSelection{Agent: request.Agent}})
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}
