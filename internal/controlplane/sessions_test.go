package controlplane

import (
	"context"
	"encoding/json"
	"fennec/internal/account"
	"io"
	"testing"
)

func TestSessionCannotSubstituteAccountOrRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "private"}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	called := false
	c := &Controller{Account: manager, Session: func(_ context.Context, principal account.Principal, runtimeID, operation string, params SessionParams) (any, error) {
		called = true
		if principal.Token != "private" || runtimeID != "owned" || operation != "list" {
			t.Error("incorrect authoritative context")
		}
		return []any{}, nil
	}}
	c.runtime = RuntimeState{Epoch: manager.Snapshot().Epoch, RuntimeID: "owned", Stage: "ready"}
	handler := c.sessionHandler("list")
	for _, raw := range []string{`{"token":"other"}`, `{"runtime_id":"other"}`, `{} {}`, `{"unknown":1}`} {
		if _, err := handler(ctx, json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if called {
		t.Fatal("invalid request reached service")
	}
	if _, err := handler(ctx, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("valid request not served")
	}
	if err := manager.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := handler(ctx, json.RawMessage(`{}`)); err == nil {
		t.Fatal("signed-out session request accepted")
	}
}

// Re-preparing the same computer (a retry, or a reconcile after a settings or
// credential change) must not refuse session operations: the broker serving
// that computer stays up while preparation runs.
func TestSessionsStayAvailableWhileTheSameComputerIsReprepared(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "private"}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	c := &Controller{
		Account: manager,
		Prepare: func(ctx context.Context, _ account.Principal, _ io.Writer) error {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil
		},
		Session: func(_ context.Context, _ account.Principal, runtimeID, _ string, _ SessionParams) (any, error) {
			return runtimeID, nil
		},
	}
	epoch := manager.Snapshot().Epoch
	c.runtime = RuntimeState{Epoch: epoch, RuntimeID: "owned", Stage: "failed"}
	if _, err := c.retryRuntime(ctx, nil); err != nil {
		t.Fatal(err)
	}
	<-started
	if state := c.runtimeSnapshot(); state.Stage != "preparing" || state.RuntimeID != "owned" {
		t.Fatalf("re-preparation dropped the computer: %+v", state)
	}
	got, err := c.sessionHandler("list")(ctx, json.RawMessage(`{}`))
	if err != nil || got != "owned" {
		t.Fatalf("sessions refused during re-preparation: %v %v", got, err)
	}
	close(release)

	// A new epoch (another principal) never inherits the previous computer.
	c.mu.Lock()
	c.runtime = RuntimeState{Epoch: epoch - 1, RuntimeID: "stale", Stage: "failed"}
	c.mu.Unlock()
	if _, err := c.ensureRuntime(ctx, nil); err != nil {
		t.Fatal(err)
	}
	<-started
	if state := c.runtimeSnapshot(); state.RuntimeID != "" {
		t.Fatalf("a new epoch inherited a computer: %+v", state)
	}
}
