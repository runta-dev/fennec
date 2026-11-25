// Package controlplane is the shared application API for native and CLI clients.
// Clients receive public state only; credentials stay with the account manager.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/account"
	"fennec/internal/agent"
	"fennec/internal/connectivity"
	"fennec/internal/control"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type Controller struct {
	Additional         map[string]control.Handler
	Services           func() any
	Settings           func() any
	UpdateState        func() any
	PreparationBlocked func() bool
	UninstallState     func() any
	LoginAllowed       func() bool
	authActions        sync.Mutex
	Account            *account.Manager
	Connectivity       *connectivity.Monitor
	Prepare            func(context.Context, account.Principal, io.Writer) error
	Session            func(context.Context, account.Principal, string, string, SessionParams) (any, error)
	// CloudUnreachable reports whether the prepared computer of an epoch has
	// stayed unreachable while this Mac is online. Optional.
	CloudUnreachable  func(epoch uint64) bool
	mu                sync.Mutex
	runtime           RuntimeState
	runtimeAgain      bool
	runtimeRetry      bool
	runtimeGeneration uint64
	runtimeCancel     context.CancelFunc
	runtimeDone       chan struct{}
	runtimeWake       chan struct{}
	maintenance       bool
	cloudPaused       bool
	cloudPausedEpoch  uint64
	// replacing holds off preparation while services bound to a cloud computer
	// deleted outside Fennec are torn down (ReplaceDeletedRuntime).
	replacing bool
}

func (c *Controller) Handlers() map[string]control.Handler {
	buildID := control.BuildID()
	executable, _ := os.Executable()
	handlers := map[string]control.Handler{
		"daemon.ping": func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"pid": os.Getpid(), "protocol": control.Version, "build_id": buildID, "executable": executable}, nil
		},
		"state.get": func(context.Context, json.RawMessage) (any, error) {
			state := map[string]any{
				"account": c.Account.Snapshot(),
				"runtime": c.runtimeSnapshot(),
				"online":  c.Connectivity == nil || c.Connectivity.Online(),
			}
			if c.Services != nil {
				state["services"] = c.Services()
			}
			if c.Settings != nil {
				state["settings"] = c.Settings()
			}
			if c.UninstallState != nil {
				state["uninstall"] = c.UninstallState()
			}
			if c.UpdateState != nil {
				state["update"] = c.UpdateState()
			}
			return state, nil
		},
		"account.status":  func(context.Context, json.RawMessage) (any, error) { return c.Account.Snapshot(), nil },
		"account.profile": func(ctx context.Context, _ json.RawMessage) (any, error) { return c.Account.Profile(ctx) },
		"account.billing": func(ctx context.Context, _ json.RawMessage) (any, error) { return c.Account.Billing(ctx) },
		"account.login": func(ctx context.Context, raw json.RawMessage) (any, error) {
			for !c.authActions.TryLock() {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(10 * time.Millisecond):
				}
			}
			defer c.authActions.Unlock()
			if c.LoginAllowed != nil && !c.LoginAllowed() {
				return nil, &control.Fault{Code: "uninstall_pending", Message: "Wait for uninstall to finish before signing in"}
			}
			if c.Account.Snapshot().Status == account.StatusPaired {
				return nil, &control.Fault{Code: "self_hosted_paired", Message: "Unpair your own cloud computer before signing in to Runta"}
			}
			var request struct {
				Reauthenticate bool `json:"reauthenticate"`
			}
			if len(raw) > 0 && json.Unmarshal(raw, &request) != nil {
				return nil, &control.Fault{Code: "invalid_params", Message: "Invalid login request"}
			}
			if request.Reauthenticate {
				state := c.Account.Reauthenticate(ctx)
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				return state, nil
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return c.Account.Login(), nil
		},
		"account.pair_device": func(ctx context.Context, _ json.RawMessage) (any, error) {
			pairing, err := c.Account.PairDevice(ctx, "Fennec on iPhone")
			switch {
			case err == nil:
				return pairing, nil
			case errors.Is(err, account.ErrPairingRateLimited):
				return nil, &control.Fault{Code: "pairing_rate_limited", Message: "Too many sign-in codes from this network; try again in 15 minutes", Retryable: true}
			default:
				// The panel shows its own copy for this code; the message helps `fen control` diagnosis.
				return nil, &control.Fault{Code: "pairing_failed", Message: "Couldn't create a sign-in code: " + err.Error(), Retryable: true}
			}
		},
		"account.logout": func(ctx context.Context, _ json.RawMessage) (any, error) {
			if err := c.Account.Logout(ctx); err != nil {
				return nil, &control.Fault{Code: "credentials_unavailable", Message: "Unable to update login credentials"}
			}
			return c.Account.Snapshot(), nil
		},
		"agents.list": func(context.Context, json.RawMessage) (any, error) { return agent.PanelList(agent.Discover()), nil },
		"state.wait": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request struct {
				Revision uint64 `json:"revision"`
			}
			if err := json.Unmarshal(raw, &request); err != nil {
				return nil, &control.Fault{Code: "invalid_params", Message: "revision required"}
			}
			changed := c.Account.Changes()
			snapshot := c.Account.Snapshot()
			if snapshot.Revision != request.Revision {
				return snapshot, nil
			}
			timer := time.NewTimer(20 * time.Second)
			defer timer.Stop()
			select {
			case <-changed:
			case <-timer.C:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return c.Account.Snapshot(), nil
		},
	}
	if c.Prepare != nil {
		handlers["runtime.ensure"] = c.ensureRuntime
		handlers["runtime.retry"] = c.retryRuntime
		handlers["runtime.status"] = func(context.Context, json.RawMessage) (any, error) { return c.runtimeSnapshot(), nil }
	}
	for method, handler := range c.Additional {
		handlers[method] = handler
	}
	if c.Session != nil {
		for _, operation := range []string{"list", "refresh", "launch", "read", "peek", "send", "stop", "attach", "resume"} {
			handlers["sessions."+operation] = c.sessionHandler(operation)
		}
	}
	handlers["api.methods"] = func(context.Context, json.RawMessage) (any, error) {
		names := make([]string, 0, len(handlers))
		for name := range handlers {
			names = append(names, name)
		}
		sort.Strings(names)
		return names, nil
	}
	for name, handler := range handlers {
		if strings.HasPrefix(name, "uninstall.") || name == "state.get" || name == "daemon.ping" || name == "api.methods" || name == "account.status" || name == "account.login" || name == "settings.get" || name == "runtime.status" || name == "logs.export" {
			continue
		}
		handlers[name] = func(ctx context.Context, raw json.RawMessage) (any, error) {
			epoch := c.Account.Snapshot().Epoch
			c.mu.Lock()
			blocked := c.maintenance || c.PreparationBlocked != nil && c.PreparationBlocked()
			cloudPaused := c.cloudPaused && c.cloudPausedEpoch == epoch
			c.mu.Unlock()
			if blocked {
				return nil, &control.Fault{Code: "uninstall_pending", Message: "Finish the pending uninstall before changing Fennec"}
			}
			if cloudPaused && (strings.HasPrefix(name, "sessions.") || strings.HasPrefix(name, "agent.login.")) {
				return nil, &control.Fault{Code: "cloud_disabled", Message: "Create a cloud computer to continue"}
			}
			return handler(ctx, raw)
		}
	}
	return handlers
}
