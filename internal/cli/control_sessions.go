package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/launcher"
	r "fennec/internal/runtime"
	"path/filepath"
)

func (s *daemonServices) session(ctx context.Context, _ string, runtimeID, operation string, params controlplane.SessionParams) (any, error) {
	s.mu.Lock()
	channel := s.channel
	config := s.config
	ready := s.brokerReady
	s.mu.Unlock()
	if channel == nil || !ready || runtimeID != config.RuntimeID {
		return nil, &control.Fault{Code: "not_ready", Message: "Runtime services are not ready", Retryable: true}
	}
	if operation == "list" {
		return channel.cachedListing(true), nil
	}
	if operation == "launch" {
		receipt, err := launcher.RequestLaunch(ctx, config.Socket, launcher.LaunchIntent{RequestID: params.SessionID, Agent: params.Agent, Directory: params.Directory, Arguments: params.Arguments})
		if err != nil {
			return nil, err
		}
		if err := launcher.SaveReceipt(filepath.Join(s.home, ".config/fennec/launches"), receipt); err != nil {
			return nil, err
		}
		channel.cachedListing(true)
		return receipt, nil
	}
	channel.mu.Lock()
	service := channel.service
	channel.mu.Unlock()
	if operation == "refresh" {
		if s.network != nil && !s.network.Online() {
			return channel.cachedListing(false), nil
		}
		service.update(ctx)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.network != nil && !s.network.Online() {
			return channel.cachedListing(false), nil
		}
		service.mu.Lock()
		problem := service.problem
		service.mu.Unlock()
		if problem != "" {
			return nil, &control.Fault{Code: "refresh_failed", Message: "Session refresh failed", Retryable: true}
		}
		return channel.cachedListing(false), nil
	}
	if operation == "attach" || operation == "resume" {
		reply := service.handle(ctx, fenServiceRequest{Operation: operation, SessionID: params.SessionID, RuntimeID: runtimeID, Address: params.Address})
		if reply.Error != "" {
			return nil, &control.Fault{Code: "attach_failed", Message: reply.Error}
		}
		return reply, nil
	}
	if operation == "stop" {
		reply := service.handle(ctx, fenServiceRequest{Operation: "kill", RuntimeID: runtimeID, SessionID: params.SessionID, Address: params.Address})
		if reply.Error != "" {
			return nil, &control.Fault{Code: "stop_unconfirmed", Message: reply.Error}
		}
		return map[string]any{"state": "finished", "session_id": params.SessionID, "timings_ms": reply.Timings}, nil
	}
	address := params.Address
	if address == "" {
		service.mu.Lock()
		for _, session := range service.sessions {
			if session.Receipt.SessionID == params.SessionID {
				address = session.Receipt.Address
				break
			}
		}
		service.mu.Unlock()
	}
	if address == "" {
		if receipt, err := launcher.ReadReceipt(filepath.Join(s.home, ".config/fennec/launches"), params.SessionID); err == nil && receipt.RuntimeID == runtimeID {
			address = receipt.Address
		}
	}
	transport, err := channel.get(ctx)
	if err != nil {
		return nil, err
	}
	result, err := transport.Operate(ctx, r.LocalOperation{Kind: operation, Address: address, SessionID: params.SessionID, Text: params.Text})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(result), nil
}

func (s *daemonServices) dispatchSession(ctx context.Context, raw json.RawMessage) (any, error) {
	var request fenServiceRequest
	if json.Unmarshal(raw, &request) != nil {
		return nil, &control.Fault{Code: "invalid_params", Message: "Invalid session request"}
	}
	lifecycle, _, epoch, ok := s.account.Session()
	if !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in first"}
	}
	s.mu.Lock()
	channel := s.channel
	runtimeID := s.config.RuntimeID
	current := s.epoch
	s.mu.Unlock()
	if current != epoch || channel == nil {
		return nil, &control.Fault{Code: "not_ready", Message: "Session service is not ready", Retryable: true}
	}
	if request.RuntimeID != "" && request.RuntimeID != runtimeID {
		return nil, &control.Fault{Code: "runtime_mismatch", Message: "Session runtime mismatch"}
	}
	request.RuntimeID = runtimeID
	channel.mu.Lock()
	service := channel.service
	channel.mu.Unlock()
	if service == nil {
		return nil, &control.Fault{Code: "not_ready", Message: "Session service is not ready", Retryable: true}
	}
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(lifecycle, cancel)
	defer stop()
	return service.handle(operation, request), nil
}
