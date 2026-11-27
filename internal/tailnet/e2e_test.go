package tailnet

import (
	"context"
	"os"
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
	defer stopDaemon(ctx, p)
	// Sign in, then Retry: each attempt must end on its own with a login URL
	// or a classified error, never wait out `tailscale up`'s 15 minutes.
	for attempt := 1; attempt <= 2; attempt++ {
		started := time.Now()
		err := RunUp(ctx, home)
		last := ReadStatus(ctx, home, false)
		t.Logf("attempt %d after %s: err=%v state=%s error=%s auth=%t", attempt, time.Since(started).Round(time.Second), err, last.State, last.Error, last.AuthURL != "")
		if !last.Installed || last.Version == "" {
			t.Fatalf("not installed: %+v", last)
		}
		if last.AuthURL == "" && last.Error == "" {
			t.Fatalf("no login URL and no error: %+v", last)
		}
		if time.Since(started) > time.Minute {
			t.Fatalf("attempt %d took %s", attempt, time.Since(started))
		}
	}
	// Cancel: Tailscale is off, with no leftover error.
	status, err := Logout(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "stopped" || status.Error != "" || responding(ctx, p) {
		t.Fatalf("after cancel: %+v responding=%t", status, responding(ctx, p))
	}
}
