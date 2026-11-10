package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func testServer(t *testing.T, handlers map[string]Handler) string {
	t.Helper()
	// Keep Unix socket paths under Darwin's 104-byte limit.
	dir, err := os.MkdirTemp("/tmp", "fen-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "control.sock")
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- Serve(ctx, path, handlers, func() { close(ready) }) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatal(err)
	case <-time.After(time.Second * 5):
		cancel()
		t.Fatal("server did not start")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second * 5):
			t.Error("server failed to stop")
		}
	})
	return path
}

func TestIndependentClientsShareControlState(t *testing.T) {
	var value atomic.Int64
	path := testServer(t, map[string]Handler{
		"increment": func(context.Context, json.RawMessage) (any, error) { return value.Add(1), nil },
		"read":      func(context.Context, json.RawMessage) (any, error) { return value.Load(), nil },
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	var got int64
	if err := (Client{path}).Call(ctx, "increment", nil, &got); err != nil || got != 1 {
		t.Fatalf("increment: %d %v", got, err)
	}
	if err := (Client{path}).Call(ctx, "read", nil, &got); err != nil || got != 1 {
		t.Fatalf("read: %d %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if err := Serve(ctx, path, nil, nil); err == nil {
		t.Fatal("second daemon accepted")
	}
}

func TestControlFaultsDoNotLeakInternalErrors(t *testing.T) {
	path := testServer(t, map[string]Handler{
		"broken": func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("secret-token") },
		"denied": func(context.Context, json.RawMessage) (any, error) {
			return nil, &Fault{Code: "denied", Message: "denied"}
		},
	})
	for method, code := range map[string]string{"broken": "internal", "denied": "denied", "missing": "method_not_found"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := (Client{path}).Call(ctx, method, nil, nil)
		cancel()
		var fault *Fault
		if !errors.As(err, &fault) || fault.Code != code {
			t.Fatalf("%s: %v", method, err)
		}
		if fault.Message == "secret-token" {
			t.Fatal("leaked secret")
		}
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	json.NewEncoder(conn).Encode(Request{Version: Version + 1, ID: "test", Method: "broken"})
	var result Response
	if err := json.NewDecoder(conn).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Error == nil || result.Error.Code != "version_mismatch" {
		t.Fatalf("%+v", result)
	}
}

func TestClientCancellationDoesNotReplayMutation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var calls atomic.Int64
	path := testServer(t, map[string]Handler{"mutate": func(ctx context.Context, _ json.RawMessage) (any, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (Client{path}).Call(ctx, "mutate", nil, nil) }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected canceled call")
		}
	case <-time.After(time.Second):
		t.Fatal("call did not cancel")
	}
	if calls.Load() != 1 {
		t.Fatal("mutation repeated")
	}
}
