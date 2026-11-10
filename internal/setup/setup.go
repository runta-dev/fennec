// Package setup reconciles a user's shared Fennec runtime.
package setup

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type Agent struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}
type State struct {
	Stage     string `json:"stage"`
	RuntimeID string `json:"runtime_id,omitempty"`
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
	Error     string `json:"error,omitempty"`
}

// Backend owns durable, user-scoped allocation. EnsureRuntime must atomically
// reuse the same runtime across devices/restarts, not just match a display name.
// InstallAgent must install the exact version and verify it before returning.
// ConfigureMagpie must configure every agent and verify the gateway routes;
// absent provider authorization is an error, never a successful setup.
type Backend interface {
	EnsureRuntime(context.Context, string) (string, error)
	WaitReady(context.Context, string) error
	SyncManifest(context.Context, string, []Agent) error
	InstallAgent(context.Context, string, Agent) error
	InstallMagpie(context.Context, string) error
	ConfigureMagpie(context.Context, string, []Agent) error
	Verify(context.Context, string, []Agent) error
}

type Coordinator struct {
	mu      sync.Mutex
	users   map[string]bool
	Backend Backend
}

// Prepare emits completed operations, not guessed percentages. Repeated runs
// reconcile the same environment through the backend's idempotent operations.
func (c *Coordinator) Prepare(ctx context.Context, user string, agents []Agent, emit func(State)) error {
	if user == "" {
		return errors.New("authenticated user ID required")
	}
	if c.Backend == nil {
		return errors.New("runtime backend is not configured")
	}
	seen := map[string]bool{}
	for _, a := range agents {
		if a.ID == "" || a.Version == "" || seen[a.ID] {
			return errors.New("unique agents with exact versions required")
		}
		seen[a.ID] = true
	}
	c.mu.Lock()
	if c.users == nil {
		c.users = map[string]bool{}
	}
	if c.users[user] {
		c.mu.Unlock()
		return errors.New("setup already in progress")
	}
	c.users[user] = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.users, user); c.mu.Unlock() }()
	state := State{Total: 6 + len(agents)}
	report := func() {
		if emit != nil {
			emit(state)
		}
	}
	step := func(name string, action func() error) error {
		state.Stage = name
		report()
		err := ctx.Err()
		if err == nil {
			err = action()
		}
		if err != nil {
			state.Stage = "failed"
			state.Error = fmt.Sprintf("%s: %v", name, err)
			report()
			return err
		}
		state.Completed++
		report()
		return nil
	}
	if err := step("allocating", func() error {
		id, err := c.Backend.EnsureRuntime(ctx, user)
		if err == nil && id == "" {
			return errors.New("empty runtime ID")
		}
		state.RuntimeID = id
		return err
	}); err != nil {
		return err
	}
	id := state.RuntimeID
	if err := step("starting", func() error { return c.Backend.WaitReady(ctx, id) }); err != nil {
		return err
	}
	if err := step("syncing_versions", func() error { return c.Backend.SyncManifest(ctx, id, agents) }); err != nil {
		return err
	}
	for _, a := range agents {
		if err := step("installing_"+a.ID, func() error { return c.Backend.InstallAgent(ctx, id, a) }); err != nil {
			return err
		}
	}
	if err := step("installing_magpie", func() error { return c.Backend.InstallMagpie(ctx, id) }); err != nil {
		return err
	}
	if err := step("configuring_magpie", func() error { return c.Backend.ConfigureMagpie(ctx, id, agents) }); err != nil {
		return err
	}
	if err := step("verifying", func() error { return c.Backend.Verify(ctx, id, agents) }); err != nil {
		return err
	}
	state.Stage = "ready"
	report()
	return nil
}
