package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/cloudstate"
	"fennec/internal/connectivity"
	"fennec/internal/control"
	"fennec/internal/runta"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

type RuntimeState struct {
	Epoch      uint64                       `json:"epoch"`
	Generation uint64                       `json:"generation"`
	Pending    bool                         `json:"pending"`
	Running    bool                         `json:"running"`
	Offline    bool                         `json:"offline,omitempty"`
	Stage      string                       `json:"stage"`
	RuntimeID  string                       `json:"runtime_id,omitempty"`
	Error      string                       `json:"error,omitempty"`
	Events     map[string]map[string]string `json:"events,omitempty"`
}

func (c *Controller) wakeChannelLocked() chan struct{} {
	if c.runtimeWake == nil {
		c.runtimeWake = make(chan struct{}, 1)
	}
	return c.runtimeWake
}

func (c *Controller) notifyRuntimeWakeLocked() {
	select {
	case c.wakeChannelLocked() <- struct{}{}:
	default:
	}
}

func (c *Controller) runtimeSnapshot() RuntimeState {
	account := c.Account.Snapshot()
	online := c.Connectivity == nil || c.Connectivity.Online()
	// Asked before taking c.mu: the services take their own lock.
	unreachable := online && c.CloudUnreachable != nil && c.CloudUnreachable(account.Epoch)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runtime.Epoch != account.Epoch {
		if account.Active() && !online {
			return RuntimeState{Epoch: account.Epoch, Offline: true, Stage: "offline"}
		}
		return RuntimeState{Epoch: account.Epoch, Stage: "idle"}
	}
	result := c.runtime
	result.Pending = c.runtimeAgain
	result.Events = make(map[string]map[string]string, len(c.runtime.Events))
	for kind, event := range c.runtime.Events {
		copy := make(map[string]string, len(event))
		for k, v := range event {
			copy[k] = v
		}
		result.Events[kind] = copy
	}
	if !online && account.Active() && result.Stage != "disabled" {
		result.Offline = true
		result.Stage = "offline"
		result.Error = ""
		result.Running = false
	}
	// A prepared computer this online Mac cannot reach is not ready. Only the
	// reported stage changes: the resident channel keeps reconnecting, and the
	// stage returns to ready as soon as it does.
	if unreachable && account.Active() && result.Stage == "ready" {
		result.Stage = "unreachable"
	}
	return result
}
func (c *Controller) ensureRuntime(_ context.Context, _ json.RawMessage) (any, error) {
	lifecycle, principal, epoch, ok := c.Account.Session()
	if !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in before preparing a runtime"}
	}
	online := c.Connectivity == nil || c.Connectivity.Refresh()
	c.mu.Lock()
	if c.cloudPausedEpoch != epoch {
		c.cloudPaused = false
	}
	if c.cloudPaused && c.cloudPausedEpoch == epoch {
		c.mu.Unlock()
		return nil, &control.Fault{Code: "cloud_disabled", Message: "Create a cloud computer to continue"}
	}
	if c.maintenance || c.PreparationBlocked != nil && c.PreparationBlocked() {
		c.mu.Unlock()
		return nil, &control.Fault{Code: "uninstall_pending", Message: "Finish the pending uninstall before starting cloud work"}
	}
	if c.replacing {
		// ReplaceDeletedRuntime starts preparation itself once the services
		// bound to the deleted computer are gone.
		c.mu.Unlock()
		return c.runtimeSnapshot(), nil
	}
	if c.runtime.Epoch == epoch && (c.runtime.Running || c.runtime.Stage == "ready") {
		c.mu.Unlock()
		return c.runtimeSnapshot(), nil
	}
	if !online {
		if c.runtime.Epoch != epoch {
			c.runtimeAgain = false
			c.runtime = RuntimeState{Epoch: epoch, Offline: true, Stage: "offline", Events: map[string]map[string]string{}}
		} else {
			c.runtime.Running = false
			c.runtime.Offline = true
			c.runtime.Stage = "offline"
			c.runtime.Error = ""
		}
		c.mu.Unlock()
		return c.runtimeSnapshot(), nil
	}
	if c.runtime.Epoch != epoch {
		c.runtimeAgain = false
	}
	// A repeated preparation in the same epoch keeps the computer it last
	// confirmed: its broker and resident channel stay usable while preparation
	// reconciles, so session operations must not be refused meanwhile. A
	// different computer replaces the ID through its runtime_id events, and
	// ReplaceDeletedRuntime clears it explicitly.
	previousID := ""
	if c.runtime.Epoch == epoch {
		previousID = c.runtime.RuntimeID
	}
	c.runtimeGeneration++
	c.runtimeRetry = false
	c.runtime = RuntimeState{Epoch: epoch, Generation: c.runtimeGeneration, Running: true, Stage: "preparing", RuntimeID: previousID, Events: map[string]map[string]string{}}
	ctx, cancel := context.WithTimeout(lifecycle, 12*time.Minute)
	done := make(chan struct{})
	c.runtimeCancel = cancel
	c.runtimeDone = done
	c.mu.Unlock()
	go func() {
		defer close(done)
		defer cancel()
		writer := &runtimeEvents{emit: func(event map[string]string) {
			c.mu.Lock()
			defer c.mu.Unlock()
			if ctx.Err() != nil || c.runtime.Epoch != epoch || c.runtimeAgain {
				return
			}
			switch event["type"] {
			case "runtime_progress", "connector_ready", "shared_services_ready", "model_catalog", "route_results", "agent_routes", "provider_catalog", "configuration_profile":
			default:
				return
			}
			c.runtime.Events[event["type"]] = event
			if id := event["runtime_id"]; id != "" {
				c.runtime.RuntimeID = id
			}
		}}
		err := c.Prepare(ctx, principal, writer)
		var api *runta.APIError
		if token, isRunta := principal.RuntaToken(); isRunta && errors.As(err, &api) && api.Status == 401 {
			c.Account.RejectCredential(token, epoch)
		}
		nowOnline := c.Connectivity == nil || c.Connectivity.Refresh()
		offline := !nowOnline || connectivity.IsOfflineError(err, nowOnline)
		c.mu.Lock()
		defer func() {
			again := c.runtimeAgain && !offline && !c.maintenance && !(c.cloudPaused && c.cloudPausedEpoch == epoch) && c.runtime.Epoch == epoch && lifecycle.Err() == nil
			if again {
				c.runtimeAgain = false
				c.runtime.Stage = "retrying"
			}
			c.notifyRuntimeWakeLocked()
			c.mu.Unlock()
			if again {
				_, _ = c.ensureRuntime(lifecycle, nil)
			}
		}()
		if c.runtime.Epoch != epoch {
			return
		}
		c.runtime.Running = false
		if errors.Is(err, cloudstate.ErrDisabled) {
			c.cloudPaused, c.cloudPausedEpoch = true, epoch
			c.runtimeAgain = false
			c.runtime.Offline = false
			c.runtime.Stage = "disabled"
			c.runtime.Error = ""
			c.runtime.Events = nil
			return
		}
		if err != nil || ctx.Err() != nil {
			c.runtimeRetry = lifecycle.Err() == nil && transientPreparationFailure(err)
			if offline && lifecycle.Err() == nil && !c.maintenance && !(c.cloudPaused && c.cloudPausedEpoch == epoch) {
				c.runtime.Offline = true
				c.runtime.Stage = "offline"
				c.runtime.Error = ""
			} else {
				c.runtime.Offline = false
				c.runtime.Stage = "failed"
				c.runtime.Error = "runtime_preparation_failed"
			}
		} else {
			c.runtime.Offline = false
			c.runtime.Error = ""
			c.runtime.Stage = "ready"
		}
	}()
	return c.runtimeSnapshot(), nil
}

// RecordEvent updates a published runtime event in place when a lightweight
// resident-channel mutation (such as single-agent model switching) succeeds
// without running a full background preparation cycle.
func (c *Controller) RecordEvent(epoch uint64, event map[string]string) bool {
	if c == nil || event["type"] == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runtime.Epoch != epoch {
		return false
	}
	if c.runtime.Events == nil {
		c.runtime.Events = map[string]map[string]string{}
	}
	copied := make(map[string]string, len(event))
	for k, v := range event {
		copied[k] = v
	}
	c.runtime.Events[event["type"]] = copied
	return true
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
	if c.Connectivity != nil {
		c.Connectivity.Start(ctx)
	}
	c.mu.Lock()
	wake := c.wakeChannelLocked()
	c.mu.Unlock()
	go func() {
		var lastEpoch uint64
		wasOnline := false
		retryDelay := 2 * time.Second
		for ctx.Err() == nil {
			changed := c.Account.Changes()
			var netChanged <-chan struct{}
			online := true
			if c.Connectivity != nil {
				netChanged = c.Connectivity.Changes()
				online = c.Connectivity.Online()
			}
			account := c.Account.Snapshot()
			var retryTimer <-chan time.Time
			var stopTimer func() bool
			if !account.Active() {
				lastEpoch = account.Epoch
				wasOnline = online
				retryDelay = 2 * time.Second
			} else if c.Prepare != nil {
				epochChanged := account.Epoch != lastEpoch
				lastEpoch = account.Epoch
				if !online {
					wasOnline = false
					retryDelay = 2 * time.Second
					c.cancelInFlightOffline(account.Epoch)
					_, _ = c.ensureRuntime(ctx, nil)
				} else {
					reconnected := !wasOnline
					wasOnline = true
					if reconnected || epochChanged {
						retryDelay = 2 * time.Second
					}
					c.mu.Lock()
					needsReconcile := c.runtime.Epoch == account.Epoch && c.runtimeAgain && !c.runtime.Running
					retryFailure := c.runtime.Epoch == account.Epoch && c.runtimeRetry
					if needsReconcile {
						c.runtimeAgain = false
						c.runtime.Stage = "retrying"
					}
					c.mu.Unlock()
					state := c.runtimeSnapshot()
					if needsReconcile || state.Stage == "idle" || state.Stage == "retrying" || (state.Stage == "offline" && (reconnected || epochChanged)) {
						_, _ = c.ensureRuntime(ctx, nil)
						state = c.runtimeSnapshot()
					}
					if (state.Stage == "offline" || state.Stage == "failed" && retryFailure) && !state.Running {
						timer := time.NewTimer(retryDelay)
						retryTimer = timer.C
						stopTimer = timer.Stop
						retryDelay = min(retryDelay*2, 5*time.Second)
					} else if state.Stage == "ready" {
						retryDelay = 2 * time.Second
					}
				}
			}
			triggeredByTimer := false
			select {
			case <-ctx.Done():
				if stopTimer != nil {
					stopTimer()
				}
				return
			case <-changed:
			case <-netChanged:
			case <-wake:
			case <-retryTimer:
				triggeredByTimer = true
			}
			if stopTimer != nil {
				stopTimer()
			}
			if triggeredByTimer && (c.Connectivity == nil || c.Connectivity.Online()) && c.Account.Snapshot().Active() && c.Prepare != nil {
				_, _ = c.ensureRuntime(ctx, nil)
			}
		}
	}()
}

// A route can remain available while its SSH connection is replaced. Retry
// finite, idempotent preparation after transport loss without requiring another
// network event or a manual click. Authentication and validation failures remain
// failures, and session launches are never replayed by this loop.
func transientPreparationFailure(err error) bool {
	for _, target := range []error{io.EOF, io.ErrUnexpectedEOF, io.ErrClosedPipe, net.ErrClosed, context.DeadlineExceeded, syscall.ECONNRESET, syscall.EPIPE, syscall.ETIMEDOUT} {
		if errors.Is(err, target) {
			return true
		}
	}
	var missing *ssh.ExitMissingError
	return errors.As(err, &missing)
}

func (c *Controller) cancelInFlightOffline(epoch uint64) {
	c.mu.Lock()
	var cancel context.CancelFunc
	if c.runtime.Epoch == epoch && c.runtime.Running {
		cancel = c.runtimeCancel
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
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

// QuiescePreparation cancels and drains the current preparation before any
// destructive runtime action. Account credentials remain available for retry.
func (c *Controller) QuiescePreparation(ctx context.Context) error {
	c.mu.Lock()
	c.maintenance = true
	c.runtimeAgain = false
	cancel, done := c.runtimeCancel, c.runtimeDone
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Controller) QuiesceForUninstall(ctx context.Context) error {
	for !c.authActions.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer c.authActions.Unlock()
	if err := c.Account.CancelAuthorization(ctx); err != nil {
		return err
	}
	return c.QuiescePreparation(ctx)
}

// QuiesceCloud pauses only the confirmed account's cloud lifecycle. It leaves
// local login and settings available, unlike the global uninstall barrier.
func (c *Controller) QuiesceCloud(ctx context.Context, epoch uint64, runtimeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _, current, ok := c.Account.Session()
	if !ok || current != epoch {
		return fmt.Errorf("cloud account changed")
	}
	c.mu.Lock()
	if c.maintenance {
		c.mu.Unlock()
		return fmt.Errorf("uninstall is in progress")
	}
	if runtimeID == "" || (c.runtime.Epoch == epoch && c.runtime.RuntimeID != "" && c.runtime.RuntimeID != runtimeID) {
		c.mu.Unlock()
		return fmt.Errorf("cloud runtime changed")
	}
	if c.runtime.Running && c.runtime.Epoch != epoch {
		c.mu.Unlock()
		return fmt.Errorf("cloud account changed")
	}
	c.cloudPaused, c.cloudPausedEpoch = true, epoch
	c.runtimeAgain = false
	cancel, done := c.runtimeCancel, c.runtimeDone
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _, current, ok = c.Account.Session()
	if !ok || current != epoch {
		return fmt.Errorf("cloud account changed")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runtime.Epoch == epoch {
		c.runtime.Running = false
		c.runtime.Stage = "disabled"
		c.runtime.Error = ""
		c.runtime.Events = nil
	}
	return nil
}

// ReplaceDeletedRuntime recovers from a cloud computer deleted outside Fennec
// (for example in the Runta console). Unlike QuiesceCloud it records no deletion
// and does not pause the lifecycle: it stops preparation, lets teardown release
// the services still bound to the deleted runtime, then prepares again so
// EnsureShared creates a replacement. A deletion made through Fennec pauses the
// cloud (cloudPaused) and is never replaced here.
func (c *Controller) ReplaceDeletedRuntime(ctx context.Context, epoch uint64, runtimeID string, teardown func(context.Context) error) error {
	_, _, current, ok := c.Account.Session()
	if !ok || current != epoch {
		return fmt.Errorf("cloud account changed")
	}
	c.mu.Lock()
	if c.maintenance || c.replacing || (c.cloudPaused && c.cloudPausedEpoch == epoch) {
		c.mu.Unlock()
		return fmt.Errorf("cloud lifecycle is busy")
	}
	if runtimeID == "" || c.runtime.Epoch != epoch || (c.runtime.RuntimeID != "" && c.runtime.RuntimeID != runtimeID) {
		c.mu.Unlock()
		return fmt.Errorf("cloud runtime changed")
	}
	c.replacing = true
	c.runtimeAgain = false
	cancel, done := c.runtimeCancel, c.runtimeDone
	c.mu.Unlock()
	finish := func(err error) error {
		c.mu.Lock()
		c.replacing = false
		if err == nil && c.runtime.Epoch == epoch && !c.runtime.Running {
			// Drop the deleted computer's ID and events; "retrying" makes the
			// next ensureRuntime prepare instead of reporting a stale "ready".
			c.runtime = RuntimeState{Epoch: epoch, Generation: c.runtimeGeneration, Stage: "retrying", Events: map[string]map[string]string{}}
		}
		c.notifyRuntimeWakeLocked()
		c.mu.Unlock()
		return err
	}
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return finish(ctx.Err())
		}
	}
	if err := finish(teardown(ctx)); err != nil {
		return err
	}
	_, err := c.ensureRuntime(ctx, nil)
	return err
}

// ResumeCloud is used only after explicit create-again authorization. It does
// not itself provision a machine or erase a persisted deletion record.
func (c *Controller) ResumeCloud(epoch uint64) error {
	_, _, current, ok := c.Account.Session()
	if !ok || current != epoch {
		return fmt.Errorf("cloud account changed")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maintenance || c.runtime.Running {
		return fmt.Errorf("cloud lifecycle is busy")
	}
	c.cloudPaused = false
	c.runtime = RuntimeState{Epoch: epoch, Generation: c.runtimeGeneration, Stage: "idle"}
	return nil
}
