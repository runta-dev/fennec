package controlplane

import (
	"context"
	"encoding/json"
	"fennec/internal/account"
	"fennec/internal/control"

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

func TestUninstallQuiesceDrainsPreparationAndPreventsRecreation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "fixture"}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	c := &Controller{Account: manager, Prepare: func(ctx context.Context, _ string, _ io.Writer) error {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return ctx.Err()
	}}
	if _, err := c.ensureRuntime(ctx, nil); err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.QuiescePreparation(ctx) }()
	<-cancelled
	select {
	case <-done:
		t.Fatal("quiesce returned before preparation cleanup")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := c.ensureRuntime(ctx, nil); err == nil {
		t.Fatal("runtime recreated while uninstalling")
	}
	if calls.Load() != 1 {
		t.Fatal("queued reconciliation escaped uninstall barrier")
	}
	_, token, _, ok := manager.Session()
	if !ok || token != "fixture" {
		t.Fatal("recovery credentials deleted before cloud confirmation")
	}
}
func TestPersistentUninstallBlocksPreparationAfterDaemonRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "fixture"}, "")
	_ = manager.Restore(ctx)
	c := &Controller{Account: manager, PreparationBlocked: func() bool { return true }, Prepare: func(context.Context, string, io.Writer) error { t.Error("recreated runtime after restart"); return nil }}
	if _, err := c.ensureRuntime(ctx, nil); err == nil {
		t.Fatal("persisted uninstall ignored")
	}
	c.Additional = map[string]control.Handler{"settings.set": func(context.Context, json.RawMessage) (any, error) {
		t.Error("settings changed during uninstall")
		return nil, nil
	}, "uninstall.plan": func(context.Context, json.RawMessage) (any, error) { return "available", nil }}
	handlers := c.Handlers()
	if _, err := handlers["settings.set"](ctx, nil); err == nil {
		t.Fatal("mutation allowed")
	}
	if _, err := handlers["uninstall.plan"](ctx, nil); err != nil {
		t.Fatal("recovery disabled", err)
	}
}
