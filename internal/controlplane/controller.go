// Package controlplane is the shared application API for native and CLI clients.
// Clients receive public state only; credentials stay with the account manager.
package controlplane

import (
	"context"
	"encoding/json"
	"fennec/internal/account"
	"fennec/internal/agent"
	"fennec/internal/control"
	"io"
	"os"
	"sort"
	"sync"
	"time"
)

type Controller struct {
	Additional   map[string]control.Handler
	Services     func() any
	Settings     func() any
	Account      *account.Manager
	Prepare      func(context.Context, string, io.Writer) error
	Session      func(context.Context, string, string, string, SessionParams) (any, error)
	mu           sync.Mutex
	runtime      RuntimeState
	runtimeAgain bool
}

func (c *Controller) Handlers() map[string]control.Handler {
	buildID := control.BuildID()
	handlers := map[string]control.Handler{
		"daemon.ping": func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"pid": os.Getpid(), "protocol": control.Version, "build_id": buildID}, nil
		},
		"state.get": func(context.Context, json.RawMessage) (any, error) {
			state := map[string]any{"account": c.Account.Snapshot(), "runtime": c.runtimeSnapshot()}
			if c.Services != nil {
				state["services"] = c.Services()
			}
			if c.Settings != nil {
				state["settings"] = c.Settings()
			}
			return state, nil
		},
		"account.status":  func(context.Context, json.RawMessage) (any, error) { return c.Account.Snapshot(), nil },
		"account.profile": func(ctx context.Context, _ json.RawMessage) (any, error) { return c.Account.Profile(ctx) },
		"account.billing": func(ctx context.Context, _ json.RawMessage) (any, error) { return c.Account.Billing(ctx) },
		"account.login":   func(context.Context, json.RawMessage) (any, error) { return c.Account.Login(), nil },
		"account.logout": func(ctx context.Context, _ json.RawMessage) (any, error) {
			if err := c.Account.Logout(ctx); err != nil {
				return nil, &control.Fault{Code: "credentials_unavailable", Message: "Unable to update login credentials"}
			}
			return c.Account.Snapshot(), nil
		},
		"agents.list": func(context.Context, json.RawMessage) (any, error) { return agent.Discover(), nil },
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
		for _, operation := range []string{"list", "refresh", "launch", "read", "send", "stop", "attach", "resume"} {
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
	return handlers
}
