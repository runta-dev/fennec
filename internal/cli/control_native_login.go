package cli

import (
	"context"
	"encoding/json"
	agents "fennec/internal/agentcmd"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/launcher"
	r "fennec/internal/runtime"
	"fennec/internal/setup"
	"os"
	"path/filepath"
)

func (s *daemonServices) nativeLogin(ctx context.Context, raw json.RawMessage, operation string, controller *controlplane.Controller) (any, error) {
	var request struct {
		Agent string `json:"agent"`
		ID    string `json:"session_id"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return nil, &control.Fault{Code: "invalid_params", Message: "Choose an agent"}
	}
	plan := setup.NativeLogin{ID: request.ID, Agent: request.Agent}
	if err := plan.Validate(); err != nil {
		return nil, &control.Fault{Code: "invalid_params", Message: "Invalid login request"}
	}
	lifecycle, _, epoch, ok := s.account.Session()
	if !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in to Runta first"}
	}
	s.mu.Lock()
	channel, config, current := s.channel, s.config, s.epoch
	s.mu.Unlock()
	if channel == nil || current != epoch {
		return nil, &control.Fault{Code: "not_ready", Message: "Cloud computer is connecting", Retryable: true}
	}
	if operation != "cancel" && newDesktopSettings(s.home).value.Magpie && !agents.NativeOnly(plan.Agent) {
		return nil, &control.Fault{Code: "mode_changed", Message: "Switch off Magpie to connect a native account"}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(lifecycle, cancel)
	defer stop()
	transport, err := channel.get(ctx)
	if err != nil {
		return nil, err
	}
	if !transport.Supports("native_login") {
		return nil, &control.Fault{Code: "not_ready", Message: "Cloud computer is updating", Retryable: true}
	}
	currentAccount := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.epoch != epoch || s.config.RuntimeID != config.RuntimeID {
			return &control.Fault{Code: "account_changed", Message: "Account changed; start sign-in again"}
		}
		return nil
	}
	receipts := filepath.Join(s.home, ".config/fennec/launches")
	if operation == "start" {
		raw, err := transport.Operate(ctx, r.LocalOperation{Kind: "launch", Launch: &r.LocalLaunch{ID: plan.ID, Agent: plan.Agent, NativeLogin: true, LocalDirectory: "/", Device: config.Device}})
		if err != nil {
			return nil, err
		}
		if err = currentAccount(); err != nil {
			return nil, err
		}
		var terminal r.LocalTerminal
		if json.Unmarshal(raw, &terminal) != nil || terminal.ID != plan.ID || terminal.Address == "" {
			return nil, &control.Fault{Code: "login_unconfirmed", Message: "Sign-in could not be opened"}
		}
		receipt := launcher.LaunchReceipt{RuntimeID: config.RuntimeID, SessionID: plan.ID, Agent: plan.Agent, Address: terminal.Address}
		if err = launcher.SaveReceipt(receipts, receipt); err != nil {
			return nil, err
		}
		return receipt, nil
	}
	receipt, err := launcher.ReadReceipt(receipts, plan.ID)
	if err != nil || receipt.RuntimeID != config.RuntimeID || receipt.Agent != plan.Agent {
		return nil, &control.Fault{Code: "invalid_login", Message: "Login session is unavailable"}
	}
	kind := "native_login_status"
	if operation == "cancel" {
		kind = "native_login_cancel"
	}
	result, err := transport.Operate(ctx, r.LocalOperation{Kind: kind, Login: &plan, Address: receipt.Address})
	if err != nil {
		return nil, err
	}
	if err = currentAccount(); err != nil {
		return nil, err
	}
	if operation == "finish" || operation == "cancel" {
		var state struct {
			SignedIn bool `json:"signed_in"`
		}
		if json.Unmarshal(result, &state) != nil {
			return nil, &control.Fault{Code: "invalid_login", Message: "Unable to check sign-in"}
		}
		if state.SignedIn {
			if err = s.acceptNativeAccounts(ctx, config.RuntimeID, map[string]string{plan.Agent: plan.ID}, controller); err != nil {
				return nil, err
			}
		}

	}
	return json.RawMessage(result), nil
}

// Session refresh already uses the resident channel. Its public account markers
// let the daemon finish onboarding even when no UI or login terminal is attached.
type nativeAccountReconciler interface {
	Reconcile(context.Context) (any, error)
}

func (s *daemonServices) acceptNativeAccounts(ctx context.Context, runtime string, accounts map[string]string, controller nativeAccountReconciler) error {
	s.nativeLoginMu.Lock()
	defer s.nativeLoginMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	user, current := s.user, s.config.RuntimeID
	s.mu.Unlock()
	if user == "" || current != runtime {
		return &control.Fault{Code: "account_changed", Message: "Account changed"}
	}
	directory := filepath.Join(s.home, ".config/fennec")
	stored := setup.NativeCloudLogins(directory, user, runtime)
	changed := false
	for agent, id := range accounts {
		if err := (setup.NativeLogin{Agent: agent, ID: id}).Validate(); err != nil {
			return err
		}
	}
	for agent, id := range accounts {
		if stored[agent] == id {
			continue
		}
		if err := setup.SaveNativeCloudPreference(directory, user, runtime, agent, id); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	if err := os.Remove(checkpointPath(s.home)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if newDesktopSettings(s.home).value.Magpie {
		hasNative := false
		for id := range accounts {
			hasNative = hasNative || agents.NativeOnly(id)
		}
		if !hasNative {
			return nil
		}
	}
	_, err := controller.Reconcile(ctx)
	return err
}
