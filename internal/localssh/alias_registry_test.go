package localssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestRegisterHostnameAliases(t *testing.T) {
	home := t.TempDir()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(pub)
	request := AliasRegistration{Alias: "Shiqis-MacBook-Pro.local", Address: "127.0.0.1:3022", Device: "aabbccdd", HostKey: string(ssh.MarshalAuthorizedKey(key))}
	alias, err := RegisterAlias(home, request)
	if err != nil || alias != request.Alias {
		t.Fatalf("%s %v", alias, err)
	}
	if got := RegisteredAlias(home, request.Device); got != alias {
		t.Fatal(got)
	}
	request.Device = "11223344"
	request.Alias = "shiqis-macbook-pro.local"
	second, err := RegisterAlias(home, request)
	if err != nil || !strings.HasPrefix(second, request.Alias+".") {
		t.Fatalf("%s %v", second, err)
	}
	request.Address = "127.0.0.1:4022"
	again, err := RegisterAlias(home, request)
	if err != nil || again != second {
		t.Fatalf("suffix changed: %s %s %v", second, again, err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".ssh/fennec/11223344.conf"))
	if !strings.Contains(string(data), "Port 4022") || !strings.Contains(string(data), "fennec-device-11223344") {
		t.Fatal(string(data))
	}
}

func TestAliasMigrationAndConcurrentAllocation(t *testing.T) {
	home := t.TempDir()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(pub)
	r := AliasRegistration{Alias: "old-mac-aabbccdd", Address: "127.0.0.1:3022", Device: "aabbccdd", HostKey: string(ssh.MarshalAuthorizedKey(key))}
	if _, err := RegisterAlias(home, r); err != nil {
		t.Fatal(err)
	}
	r.Alias = "Mac.local"
	if a, err := RegisterAlias(home, r); err != nil || a != r.Alias {
		t.Fatalf("%s %v", a, err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".ssh/fennec/aabbccdd.conf"))
	if !strings.Contains(string(data), "old-mac-aabbccdd") {
		t.Fatal("lost compatibility alias")
	}
	var wg sync.WaitGroup
	for _, id := range []string{"11111111", "22222222", "33333333", "44444444"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			req := r
			req.Device = id
			if _, err := RegisterAlias(home, req); err != nil {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, id := range []string{"aabbccdd", "11111111", "22222222", "33333333", "44444444"} {
		a := strings.ToLower(RegisteredAlias(home, id))
		if seen[a] {
			t.Fatalf("collision %s", a)
		}
		seen[a] = true
	}
}
