package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	r "fennec/internal/runtime"
	"fennec/internal/setup"
)

func (s *daemonServices) getNativeModels(ctx context.Context, raw json.RawMessage) (any, error) {
	return s.nativeModelOperation(ctx, raw, "native_models")
}
func (s *daemonServices) setNativeModel(ctx context.Context, raw json.RawMessage) (any, error) {
	return s.nativeModelOperation(ctx, raw, "native_model_set")
}
func (s *daemonServices) nativeModelOperation(ctx context.Context, raw json.RawMessage, kind string) (any, error) {
	var choice setup.NativeModelSelection
	if json.Unmarshal(raw, &choice) != nil {
		return nil, &control.Fault{Code: "invalid_params", Message: "Choose an agent"}
	}
	operation := r.LocalOperation{Kind: kind, NativeModel: &choice}
	if err := operation.Validate(); err != nil {
		return nil, &control.Fault{Code: "invalid_params", Message: err.Error()}
	}
	lifecycle, _, epoch, ok := s.account.Session()
	if !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in first"}
	}
	s.mu.Lock()
	channel, current := s.channel, s.epoch
	s.mu.Unlock()
	if channel == nil || current != epoch {
		return nil, &control.Fault{Code: "not_ready", Message: "Cloud computer is connecting", Retryable: true}
	}
	if newDesktopSettings(s.home).value.Magpie {
		return nil, &control.Fault{Code: "mode_changed", Message: "Model settings changed; reopen the agent"}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(lifecycle, cancel)
	defer stop()
	transport, err := channel.get(ctx)
	if err != nil {
		return nil, err
	}
	if !transport.Supports(kind) {
		return nil, &control.Fault{Code: "not_ready", Message: "Cloud computer is updating", Retryable: true}
	}
	result, err := transport.Operate(ctx, operation)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return json.RawMessage(result), nil
}
