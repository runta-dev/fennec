package tailnet

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSignInEndToEnd downloads Tailscale, starts tailscaled in a temporary
// home and runs `tailscale up` until it reports a login URL or a classified
// error. It needs the network: FENNEC_TAILNET_E2E=1 go test -run EndToEnd.
func TestSignInEndToEnd(t *testing.T) {
	if os.Getenv("FENNEC_TAILNET_E2E") == "" {
		t.Skip("set FENNEC_TAILNET_E2E=1")
	}
	systemd = func() bool { return false }
	home := t.TempDir()
	p := paths{home}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunUp(ctx, home) }()
	defer cancel()
	var last Status
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		last = ReadStatus(ctx, home, false)
		t.Logf("state=%s error=%s auth=%t version=%s", last.State, last.Error, last.AuthURL != "", last.Version)
		if last.AuthURL != "" || last.Error != "" {
			break
		}
	}
	if !last.Installed || last.Version == "" {
		t.Fatalf("not installed: %+v", last)
	}
	if last.AuthURL == "" && last.Error == "" {
		t.Fatalf("no login URL and no error: %+v", last)
	}
	if last.AuthURL != "" && !strings.HasPrefix(last.AuthURL, "https://login.tailscale.com/") {
		t.Fatalf("auth URL = %q", last.AuthURL)
	}
	t.Logf("final: state=%s error=%s detail=%s auth=%s", last.State, last.Error, last.Detail, last.AuthURL)
	cancel()
	stopDaemon(t, p)
}

func stopDaemon(t *testing.T, p paths) {
	data, err := os.ReadFile(p.pidFile())
	if err != nil {
		t.Fatalf("tailscaled pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		t.Fatalf("tailscaled pid = %q", data)
	}
	syscall.Kill(pid, syscall.SIGTERM)
}
