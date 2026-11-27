package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthorizeTailnetKey(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".ssh"), 0700)
	path := filepath.Join(home, ".ssh/authorized_keys")
	// An existing key without a trailing newline must stay intact.
	os.WriteFile(path, []byte("ssh-ed25519 AAAAexisting laptop"), 0600)
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2r0BSzk7ru9MVrZQ6jZnfdNnL7bpBuu8ilmgEqvPCw fennec-cloud-tailnet"
	for i := 0; i < 2; i++ {
		if err := authorizeTailnetKey(home, key); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(path)
	want := "ssh-ed25519 AAAAexisting laptop\n" + `from="100.64.0.0/10,fd7a:115c:a1e0::/48" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2r0BSzk7ru9MVrZQ6jZnfdNnL7bpBuu8ilmgEqvPCw fennec-cloud-tailnet` + "\n"
	if string(data) != want {
		t.Fatalf("authorized_keys = %q", data)
	}
	if authorizeTailnetKey(home, "not a key") == nil || authorizeTailnetKey(home, `command="x" AAAA`) == nil {
		t.Fatal("invalid key accepted")
	}
}

func TestLocalShieldsUpMarksThisDevice(t *testing.T) {
	saved := tailscalePrefs
	t.Cleanup(func() { tailscalePrefs = saved; shieldsCache.at = time.Time{} })
	for _, tc := range []struct {
		prefs string
		err   error
		want  bool
	}{
		{`{"ShieldsUp": true, "RunSSH": false}`, nil, true},
		{`{"ShieldsUp": false}`, nil, false},
		{``, os.ErrNotExist, false},
	} {
		shieldsCache.at = time.Time{}
		tailscalePrefs = func(context.Context) ([]byte, error) { return []byte(tc.prefs), tc.err }
		if got := localShieldsUp(); got != tc.want {
			t.Fatalf("prefs %q: shields up = %v", tc.prefs, got)
		}
	}
	status := map[string]any{"devices": []any{map[string]any{"id": "a"}, map[string]any{"id": "b", "computer": map[string]any{"error": "unreachable", "user": "me"}}}}
	markThisDevice(status, 1, "shields_up")
	computer := status["devices"].([]any)[1].(map[string]any)["computer"].(map[string]any)
	if computer["error"] != "shields_up" || computer["user"] != "me" {
		t.Fatalf("computer = %v", computer)
	}
	markThisDevice(status, 0, "shields_up")
	if status["devices"].([]any)[0].(map[string]any)["computer"].(map[string]any)["error"] != "shields_up" {
		t.Fatal("device without a computer not marked")
	}
}
