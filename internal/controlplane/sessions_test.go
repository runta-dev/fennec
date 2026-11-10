package controlplane

import (
	"context"
	"encoding/json"
	"fennec/internal/account"
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
	c := &Controller{Account: manager, Session: func(_ context.Context, token, runtimeID, operation string, params SessionParams) (any, error) {
		called = true
		if token != "private" || runtimeID != "owned" || operation != "list" {
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
