package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fmt"
	"sync"
	"time"
)

type RuntimeState struct {
	Epoch     uint64                       `json:"epoch"`
	Running   bool                         `json:"running"`
	Stage     string                       `json:"stage"`
	RuntimeID string                       `json:"runtime_id,omitempty"`
	Error     string                       `json:"error,omitempty"`
	Events    map[string]map[string]string `json:"events,omitempty"`
}

func (c *Controller) runtimeSnapshot() RuntimeState {
	account := c.Account.Snapshot()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runtime.Epoch != account.Epoch {
		return RuntimeState{Epoch: account.Epoch, Stage: "idle"}
	}
	result := c.runtime
	result.Events = make(map[string]map[string]string, len(c.runtime.Events))
	for kind, event := range c.runtime.Events {
		copy := make(map[string]string, len(event))
		for k, v := range event {
			copy[k] = v
		}
		result.Events[kind] = copy
	}
	return result
}
func (c *Controller) ensureRuntime(_ context.Context, _ json.RawMessage) (any, error) {
	lifecycle, token, epoch, ok := c.Account.Session()
	if !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in before preparing a runtime"}
	}
	c.mu.Lock()
	if c.runtime.Epoch == epoch && (c.runtime.Running || c.runtime.Stage == "ready") {
		c.mu.Unlock()
		return c.runtimeSnapshot(), nil
	}
	if c.runtime.Epoch != epoch {
		c.runtimeAgain = false
	}
	c.runtime = RuntimeState{Epoch: epoch, Running: true, Stage: "preparing", Events: map[string]map[string]string{}}
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(lifecycle, 12*time.Minute)
		defer cancel()
		writer := &runtimeEvents{emit: func(event map[string]string) {
			c.mu.Lock()
			defer c.mu.Unlock()
			if ctx.Err() != nil || c.runtime.Epoch != epoch {
				return
			}
			switch event["type"] {
			case "runtime_progress", "connector_ready", "shared_services_ready", "model_catalog", "route_results", "agent_routes", "provider_catalog":
			default:
				return
			}
			c.runtime.Events[event["type"]] = event
			if id := event["runtime_id"]; id != "" {
				c.runtime.RuntimeID = id
			}
		}}
		err := c.Prepare(ctx, token, writer)
		c.mu.Lock()
		defer func() {
			again := c.runtimeAgain && c.runtime.Epoch == epoch && lifecycle.Err() == nil
			if again {
				c.runtimeAgain = false
				c.runtime.Stage = "retrying"
			}
			c.mu.Unlock()
			if again {
				_, _ = c.ensureRuntime(lifecycle, nil)
			}
		}()
		if c.runtime.Epoch != epoch {
			return
		}
		c.runtime.Running = false
		if err != nil || ctx.Err() != nil {
			c.runtime.Stage = "failed"
			c.runtime.Error = "runtime_preparation_failed"
		} else {
			c.runtime.Stage = "ready"
		}
	}()
	return c.runtimeSnapshot(), nil
}

// Reconcile coalesces configuration changes that arrive during preparation.
func (c *Controller) Reconcile(ctx context.Context) (any, error) {
	c.mu.Lock()
	if c.runtime.Running {
		c.runtimeAgain = true
		c.mu.Unlock()
		return c.runtimeSnapshot(), nil
	}
	c.runtime.Stage = "retrying"
	c.mu.Unlock()
	return c.ensureRuntime(ctx, nil)
}

type runtimeEvents struct {
	mu      sync.Mutex
	pending []byte
	emit    func(map[string]string)
}

func (w *runtimeEvents) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending)+len(data) > 4<<20 {
		return 0, fmt.Errorf("runtime event too large")
	}
	w.pending = append(w.pending, data...)
	for {
		index := bytes.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		var event map[string]string
		if json.Unmarshal(w.pending[:index], &event) != nil {
			return 0, fmt.Errorf("invalid runtime event")
		}
		w.pending = w.pending[index+1:]
		w.emit(event)
	}
	return len(data), nil
}

func (c *Controller) retryRuntime(ctx context.Context, raw json.RawMessage) (any, error) {
	c.mu.Lock()
	if c.runtime.Running {
		c.mu.Unlock()
		return nil, &control.Fault{Code: "busy", Message: "Runtime preparation is already running", Retryable: true}
	}
	c.runtime.Stage = "retrying"
	c.mu.Unlock()
	return c.ensureRuntime(ctx, raw)
}

// Start restores runtime services after daemon startup or a successful device
// login. A GUI connection is not part of this lifecycle.
func (c *Controller) Start(ctx context.Context) {
	go func() {
		for ctx.Err() == nil {
			changed := c.Account.Changes()
			if c.Account.Snapshot().Status == "authorized" && c.Prepare != nil {
				_, _ = c.ensureRuntime(ctx, nil)
			}
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
		}
	}()
}

// Selecting cloud records intent even while setup is in progress. It starts
// recovery after a failure, without scheduling duplicate work during setup.
func (c *Controller) PrepareIfIdle(ctx context.Context) (any, error) {
	c.mu.Lock()
	if c.runtime.Running {
		c.mu.Unlock()
		return c.runtimeSnapshot(), nil
	}
	c.runtime.Stage = "retrying"
	c.mu.Unlock()
	return c.ensureRuntime(ctx, nil)
}
