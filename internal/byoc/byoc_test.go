package byoc_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fennec/internal/byoc"
	"fennec/internal/byoc/byoctest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func otherKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestDiscoverPairsOnlyTheVerifiedHostKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	home := t.TempDir()
	computer := byoctest.New(t)
	computer.Authorize(t, home)

	if _, _, err := byoc.Discover(ctx, home, computer.Endpoint(), ""); err == nil {
		t.Fatal("paired without a verified fingerprint")
	}
	if _, _, err := byoc.Discover(ctx, home, computer.Endpoint(), ssh.FingerprintSHA256(otherKey(t).PublicKey())); err == nil {
		t.Fatal("paired with a host key that does not match the fingerprint")
	}

	pairing, client, err := byoc.Discover(ctx, home, computer.Endpoint(), computer.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	if pairing.MachineID != byoctest.Machine || pairing.User != byoc.DefaultUser {
		t.Fatalf("unexpected pairing: %+v", pairing)
	}
	pinned, err := pairing.PublicHostKey()
	if err != nil || ssh.FingerprintSHA256(pinned) != computer.Fingerprint() {
		t.Fatal("pairing did not pin the verified host key")
	}

	// The full public key is accepted as an expectation too.
	if _, client, err := byoc.Discover(ctx, home, computer.Endpoint(), byoc.AuthorizedKey(computer.HostSigner)); err != nil {
		t.Fatal(err)
	} else {
		client.Close()
	}
}

func TestDiscoverRejectsComputerWithoutIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	home := t.TempDir()
	computer := byoctest.New(t)
	computer.Authorize(t, home)
	for _, identity := range []string{"", "not json", `{"machine_id":"VPS-1"}`, `{"machine_id":""}`} {
		computer.SetIdentity(identity)
		if _, _, err := byoc.Discover(ctx, home, computer.Endpoint(), computer.Fingerprint()); err == nil {
			t.Fatalf("paired with identity %q", identity)
		}
	}
}

func TestConnectRequiresPinnedKeyAndSameMachine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	home := t.TempDir()
	computer := byoctest.New(t)
	pairing := computer.Pair(t, home)

	client, err := byoc.Connect(ctx, home, pairing)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()

	// A replaced volume reports a different machine.
	computer.SetIdentity(`{"machine_id":"1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"}`)
	if _, err := byoc.Connect(ctx, home, pairing); !errors.Is(err, byoc.ErrIdentityMismatch) {
		t.Fatalf("expected identity mismatch, got %v", err)
	}
	computer.SetIdentity(`{"machine_id":"` + byoctest.Machine + `"}`)

	// A different host key at the same address is refused.
	forged := pairing
	forged.HostKey = byoc.AuthorizedKey(otherKey(t))
	if _, err := byoc.Connect(ctx, home, forged); err == nil {
		t.Fatal("connected despite a host key mismatch")
	}
}

func TestSaveLoadRemove(t *testing.T) {
	home := t.TempDir()
	if _, err := byoc.Load(home); !errors.Is(err, byoc.ErrNotPaired) {
		t.Fatalf("expected not paired, got %v", err)
	}
	p := byoc.Pairing{Version: 1, MachineID: byoctest.Machine, Host: "vps.example.com", Port: 2222, User: byoc.DefaultUser, HostKey: byoc.AuthorizedKey(otherKey(t)), PairedAt: time.Unix(1, 0).UTC()}
	if err := byoc.Save(home, p); err != nil {
		t.Fatal(err)
	}
	loaded, err := byoc.Load(home)
	if err != nil || loaded != p {
		t.Fatalf("round trip changed the record: %+v %v", loaded, err)
	}
	for _, name := range []string{"pairing.json", "known_hosts"} {
		info, err := os.Stat(filepath.Join(byoc.Dir(home), name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s is not private", name)
		}
	}
	known, _ := os.ReadFile(filepath.Join(byoc.Dir(home), "known_hosts"))
	if !strings.HasPrefix(string(known), "[vps.example.com]:2222 ssh-ed25519 ") {
		t.Fatalf("unexpected known_hosts entry %q", known)
	}
	if err := byoc.Remove(home); err != nil {
		t.Fatal(err)
	}
	if _, err := byoc.Load(home); !errors.Is(err, byoc.ErrNotPaired) {
		t.Fatal("record survived removal")
	}
}

func TestValidateRejectsUnsafeRecords(t *testing.T) {
	good := byoc.Pairing{Version: 1, MachineID: byoctest.Machine, Host: "10.0.0.5", Port: 22, User: byoc.DefaultUser, HostKey: byoc.AuthorizedKey(otherKey(t))}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*byoc.Pairing){
		"version":     func(p *byoc.Pairing) { p.Version = 2 },
		"machine":     func(p *byoc.Pairing) { p.MachineID = "vps-1" },
		"host option": func(p *byoc.Pairing) { p.Host = "-oProxyCommand=x" },
		"host space":  func(p *byoc.Pairing) { p.Host = "a b" },
		"host user":   func(p *byoc.Pairing) { p.Host = "root@host" },
		"port":        func(p *byoc.Pairing) { p.Port = 0 },
		"user":        func(p *byoc.Pairing) { p.User = "Root" },
		"host key":    func(p *byoc.Pairing) { p.HostKey = "not a key" },
		"two keys":    func(p *byoc.Pairing) { p.HostKey = good.HostKey + "\n" + good.HostKey },
	} {
		p := good
		mutate(&p)
		if p.Validate() == nil {
			t.Fatalf("accepted unsafe record: %s", name)
		}
	}
}
