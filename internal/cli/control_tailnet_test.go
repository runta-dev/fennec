package cli

import (
	"os"
	"path/filepath"
	"testing"
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
