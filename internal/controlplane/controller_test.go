package controlplane

import (
	"context"
	"encoding/json"
	"fennec/internal/account"
	"fennec/internal/control"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type testCredentials struct {
	mu    sync.Mutex
	token string
}

func (c *testCredentials) Read(context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token, nil
}
func (c *testCredentials) Write(_ context.Context, s string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = s
	return nil
}
func (c *testCredentials) Delete(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	return nil
}

func TestUIAndCLIUseOneAccountControlPlane(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "private"}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "fen-api-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "control.sock")
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- control.Serve(ctx, socket, (&Controller{Account: manager}).Handlers(), func() { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(time.Second * 3):
		t.Fatal("start timeout")
	}
	ui, cli := control.Client{Socket: socket}, control.Client{Socket: socket}
	var before account.Snapshot
	if err := ui.Call(ctx, "account.status", nil, &before); err != nil {
		t.Fatal(err)
	}
	if before.Status != "authorized" {
		t.Fatal(before)
	}
	if err := cli.Call(ctx, "account.logout", nil, nil); err != nil {
		t.Fatal(err)
	}
	var after account.Snapshot
	if err := ui.Call(ctx, "account.status", nil, &after); err != nil {
		t.Fatal(err)
	}
	if after.Status != "signed_out" || after.Epoch <= before.Epoch {
		t.Fatal(after)
	}
	var methods []string
	if err := cli.Call(ctx, "api.methods", json.RawMessage(`{}`), &methods); err != nil {
		t.Fatal(err)
	}
	if len(methods) < 6 {
		t.Fatal(methods)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second * 3):
		t.Fatal("stop timeout")
	}
}
