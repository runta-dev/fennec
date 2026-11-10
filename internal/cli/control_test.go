package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/control"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFenControlUsesDaemonAPI(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fen-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	socket := filepath.Join(home, ".config/fennec/ipc/control.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- control.Serve(ctx, socket, map[string]control.Handler{"daemon.ping": func(context.Context, json.RawMessage) (any, error) { return nil, nil }, "test.echo": func(_ context.Context, raw json.RawMessage) (any, error) { return raw, nil }}, func() { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(time.Second * 3):
		t.Fatal("start timeout")
	}
	var output bytes.Buffer
	if err := callControl([]string{"test.echo"}, strings.NewReader(`{"from":"cli"}`), &output); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != `{"from":"cli"}` {
		t.Fatal(output.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
