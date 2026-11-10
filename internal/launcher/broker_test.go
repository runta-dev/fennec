package launcher

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrokerReturnsFailureReason(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "fn-broker-error-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "ipc")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ServeBroker(ctx, path, func(context.Context, LaunchIntent) (LaunchReceipt, error) {
			return LaunchReceipt{}, errors.New("cloud agent model is not configured")
		})
	}()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	request := LaunchIntent{RequestID: strings.Repeat("a", 32), Agent: "codex", Directory: "/project"}
	_, err = RequestLaunch(ctx, path, request)
	if err == nil || !strings.Contains(err.Error(), "cloud agent model is not configured") {
		t.Fatal("launch failure reason was hidden", err)
	}
}

func TestBrokerReturnsReceiptWithoutCredentials(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "fn-broker-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "ipc")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ServeBroker(ctx, path, func(_ context.Context, r LaunchIntent) (LaunchReceipt, error) {
			return LaunchReceipt{RuntimeID: "runtime", Address: "t7", SessionID: r.RequestID, Agent: r.Agent}, nil
		})
	}()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	request := LaunchIntent{RequestID: strings.Repeat("a", 32), Agent: "codex", Directory: "/project", Arguments: []string{"literal $(no)"}}
	receipt, err := RequestLaunch(ctx, path, request)
	if err != nil || receipt.Address != "t7" {
		t.Fatal(receipt, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("socket permissions")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker did not stop")
	}
}
func TestBrokerRejectsPublicDirectory(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0755)
	err := ServeBroker(context.Background(), filepath.Join(dir, "ipc"), nil)
	if err == nil {
		t.Fatal("public directory accepted")
	}
}

func TestBrokerRecoversStaleSocketAndRejectsSecondOwner(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "fn-recover-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "ipc")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	stale.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- ServeBrokerReady(ctx, path, nil, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("recovery timed out")
	}
	if err := ServeBroker(ctx, path, nil); err == nil {
		t.Fatal("second owner accepted")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal("first listener lost", err)
	}
	conn.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
