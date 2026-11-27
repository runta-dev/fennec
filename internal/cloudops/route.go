package cloudops

import (
	"context"
	"encoding/json"
	"fennec/internal/board"
	"fennec/internal/gateway"
	"fennec/internal/localssh"
	"fennec/internal/queuescheduler"
	"fennec/internal/runtime"
	"fennec/internal/setup"
	"fennec/internal/tailnet"
	"fmt"
	"path/filepath"
)

// Operate validates one operation and hands it to the package that owns it.
func Operate(ctx context.Context, r *runtime.LocalRuntime, o Operation) (json.RawMessage, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	switch o.Kind {
	case "thread_session":
		terminal, err := queuescheduler.ThreadSession(ctx, r, board.Store{Home: r.Home, Role: "cloud"}, o.Project, o.RequestNumber)
		if err != nil {
			return nil, err
		}
		return json.Marshal(terminal)
	case "connector_health", "mac_online":
		if err := localssh.ProbeMac(ctx, r.Home, *o.Health); err != nil {
			return nil, err
		}
		if o.Kind == "mac_online" {
			return r.NotifyMacOnline(ctx, *o.Health)
		}
		return json.RawMessage(`{"healthy":true}`), nil
	case "preparation_health":
		if err := setup.CheckPreparationHealth(ctx, r.Home, *o.Preparation); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"healthy":true}`), nil
	case "system_metrics":
		return json.Marshal(runtime.CollectSystemMetrics())
	case "tailnet_status":
		return json.Marshal(tailnet.ReadStatus(ctx, r.Home, o.Tailnet != nil && o.Tailnet.Probe))
	case "tailnet_login":
		return marshal(tailnet.StartLogin(ctx, r.Home))
	case "tailnet_logout":
		return marshal(tailnet.Logout(ctx, r.Home))
	case "tailnet_add":
		return marshal(tailnet.Add(ctx, r.Home, *o.Tailnet))
	case "tailnet_remove":
		return marshal(tailnet.Remove(ctx, r.Home, *o.Tailnet))
	case "cloud_agents":
		return json.Marshal(setup.CloudAgents(r.Home))
	case "reconfigure_runtime":
		return marshal(setup.ReconfigureRuntime(ctx, r.Home, *o.Configuration))
	case "native_account":
		return marshal(setup.NativeCloudAccount(r.Home, o.NativeModel.Agent))
	case "native_login_status", "native_login_cancel":
		return nativeLogin(ctx, r, o)
	case "native_models", "native_model_set":
		if !setup.NativeAgent(r.Home, o.NativeModel.Agent) {
			return nil, fmt.Errorf("native models unavailable in gateway mode")
		}
		if o.Kind == "native_models" {
			return marshal(setup.NativeModels(ctx, r.Home, o.NativeModel.Agent, true))
		}
		return marshal(setup.SetNativeModel(ctx, r.Home, *o.NativeModel))
	// One Mac credential applied with the same rules and baseline as a full
	// sync, and the long-lived watch that reports cloud refreshes to the Mac.
	case "native_credential_set":
		return marshal(setup.ApplyNativeCredential(ctx, r.Home, *o.Credential))
	case "native_credential_watch":
		return marshal(setup.WatchNativeCredentials(ctx, r.Home, *o.CredentialWatch))
	case "model_catalog":
		return marshal(setup.LocalGatewayModels(ctx))
	case "model_accounts":
		return marshal(setup.ApplyModelAccounts(ctx, r.Home, *o.Accounts))
	case "magpie_credentials_sync":
		return marshal(setup.SyncMagpieCredentials(ctx, r.Home, o.MagpieCredentials))
	case "connector_prepare":
		return marshal(localssh.BootstrapExistingLink(ctx, r.Home, *o.Connector))
	case "reconcile_routes":
		return marshal(setup.ReconcileLocalRoutes(ctx, r.Home, o.Routes))
	case "list":
		return List(ctx, r)
	case "launch":
		return marshal(r.Launch(ctx, *o.Launch))
	case "stop":
		return marshal(r.Stop(ctx, o.Address, o.SessionID))
	case "read", "peek":
		return r.Read(ctx, o.Address, o.SessionID, o.Kind == "peek")
	case "send":
		return r.Send(ctx, o.Address, o.SessionID, o.Text)
	}
	return nil, fmt.Errorf("unsupported operation")
}

// List is the session listing plus the cloud's account and gateway state,
// which every client shows next to its sessions.
func List(ctx context.Context, r *runtime.LocalRuntime) (json.RawMessage, error) {
	raw, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	var listing map[string]json.RawMessage
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, err
	}
	if accounts, e := setup.SelectedNativeLogins(r.Home); e == nil && len(accounts) > 0 {
		listing["native_accounts"], _ = json.Marshal(accounts)
	}
	listing["gateway"], _ = json.Marshal(map[string]string{"status": gateway.ReadState(r.Home).Status})
	return json.Marshal(listing)
}

func nativeLogin(ctx context.Context, r *runtime.LocalRuntime, o Operation) (json.RawMessage, error) {
	terminal, err := r.Terminal(ctx, o.Address, o.Login.ID)
	if err != nil {
		return nil, err
	}
	if o.Kind == "native_login_cancel" {
		if err := setup.CancelNativeLogin(ctx, r.Home, *o.Login); err != nil {
			root, selected, readErr := setup.NativeAccountRoot(r.Home, o.Login.Agent)
			if readErr == nil && selected && filepath.Base(root) == o.Login.ID {
				return json.Marshal(map[string]bool{"signed_in": true})
			}
			return nil, err
		}
		if _, err := r.Stop(ctx, o.Address, o.Login.ID); err != nil {
			return nil, err
		}
		if err := setup.DiscardNativeLogin(ctx, r.Home, *o.Login); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]bool{"canceled": true})
	}
	if terminal.State != "live" && terminal.State != "finished" {
		return nil, fmt.Errorf("login terminal state unavailable")
	}
	root, selected, err := setup.NativeAccountRoot(r.Home, o.Login.Agent)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"signed_in": selected && filepath.Base(root) == o.Login.ID, "running": terminal.State == "live", "agent": o.Login.Agent, "session_id": o.Login.ID})
}

func marshal[T any](value T, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// Operator binds Operate to one runtime, for terminalstream's server.
func Operator(r *runtime.LocalRuntime) func(context.Context, Operation) (json.RawMessage, error) {
	return func(ctx context.Context, o Operation) (json.RawMessage, error) { return Operate(ctx, r, o) }
}
