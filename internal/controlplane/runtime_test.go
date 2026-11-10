package controlplane

import (
	"context"
	"encoding/json"
	"fennec/internal/account"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreparationSingleFlightAndLogoutCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "private"}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	entered, stopped := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	c := &Controller{Account: manager, Prepare: func(ctx context.Context, token string, w io.Writer) error {
		if token != "private" {
			t.Error("incorrect credential")
		}
		calls.Add(1)
		close(entered)
		io.WriteString(w, "{\"type\":\"runtime_progress\",\"runtime_id\":\"shared\",\"stage\":\"connecting\"}\n")
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}}
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := c.ensureRuntime(ctx, json.RawMessage(`{}`)); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("preparation did not start")
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate preparation", calls.Load())
	}
	if err := manager.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("old account preparation survived logout")
	}
	if got := c.runtimeSnapshot(); got.Stage != "idle" || got.RuntimeID != "" {
		t.Fatalf("old account runtime leaked: %+v", got)
	}
	if _, err := c.ensureRuntime(ctx, nil); err == nil {
		t.Fatal("signed-out preparation allowed")
	}
}
