package byoc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const testMachine = "0b9f6c1e-3d2a-4c5b-9e8f-7a6b5c4d3e2f"

// fakeComputer is an SSH server standing in for the cloud image: it accepts
// one authorized key and answers the identity read.
type fakeComputer struct {
	listener   net.Listener
	hostSigner ssh.Signer
	authorized atomic.Value // ssh.PublicKey
	identity   atomic.Value // string
}

func newFakeComputer(t *testing.T) *fakeComputer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := &fakeComputer{listener: listener, hostSigner: hostSigner}
	c.identity.Store(`{"machine_id":"` + testMachine + `"}`)
	config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		allowed, _ := c.authorized.Load().(ssh.PublicKey)
		if meta.User() != DefaultUser || allowed == nil || string(allowed.Marshal()) != string(key.Marshal()) {
			return nil, errors.New("unauthorized")
		}
		return nil, nil
	}}
	config.AddHostKey(hostSigner)
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go c.serve(raw, config)
		}
	}()
	t.Cleanup(func() { listener.Close() })
	return c
}

func (c *fakeComputer) serve(raw net.Conn, config *ssh.ServerConfig) {
	conn, channels, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		raw.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(requests)
	for opened := range channels {
		channel, reqs, err := opened.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for req := range reqs {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				length := binary.BigEndian.Uint32(req.Payload[:4])
				command := string(req.Payload[4 : 4+length])
				req.Reply(true, nil)
				status := uint32(0)
				if command == `cat -- "$HOME/`+IdentityPath+`"` {
					channel.Write([]byte(c.identity.Load().(string)))
				} else {
					status = 127
				}
				channel.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, status))
				return
			}
		}()
	}
}

func (c *fakeComputer) endpoint() Endpoint {
	host, port, _ := net.SplitHostPort(c.listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return Endpoint{Host: host, Port: p, User: DefaultUser}
}

func (c *fakeComputer) authorize(t *testing.T, home string) {
	t.Helper()
	signer, err := DeviceKey(home)
	if err != nil {
		t.Fatal(err)
	}
	c.authorized.Store(signer.PublicKey())
}

func TestDiscoverPairsOnlyTheVerifiedHostKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	home := t.TempDir()
	computer := newFakeComputer(t)
	computer.authorize(t, home)

	if _, _, err := Discover(ctx, home, computer.endpoint(), ""); err == nil {
		t.Fatal("paired without a verified fingerprint")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	if _, _, err := Discover(ctx, home, computer.endpoint(), ssh.FingerprintSHA256(otherSigner.PublicKey())); err == nil {
		t.Fatal("paired with a host key that does not match the fingerprint")
	}

	fingerprint := ssh.FingerprintSHA256(computer.hostSigner.PublicKey())
	pairing, client, err := Discover(ctx, home, computer.endpoint(), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	if pairing.MachineID != testMachine || pairing.User != DefaultUser {
		t.Fatalf("unexpected pairing: %+v", pairing)
	}
	pinned, err := pairing.PublicHostKey()
	if err != nil || ssh.FingerprintSHA256(pinned) != fingerprint {
		t.Fatal("pairing did not pin the verified host key")
	}

	// The full public key is accepted as an expectation too.
	full := AuthorizedKey(computer.hostSigner)
	if _, client, err := Discover(ctx, home, computer.endpoint(), full); err != nil {
		t.Fatal(err)
	} else {
		client.Close()
	}
}

func TestDiscoverRejectsComputerWithoutIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	home := t.TempDir()
	computer := newFakeComputer(t)
	computer.authorize(t, home)
	fingerprint := ssh.FingerprintSHA256(computer.hostSigner.PublicKey())
	for _, identity := range []string{"", "not json", `{"machine_id":"VPS-1"}`, `{"machine_id":""}`} {
		computer.identity.Store(identity)
		if _, _, err := Discover(ctx, home, computer.endpoint(), fingerprint); err == nil {
			t.Fatalf("paired with identity %q", identity)
		}
	}
}

func TestConnectRequiresPinnedKeyAndSameMachine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	home := t.TempDir()
	computer := newFakeComputer(t)
	computer.authorize(t, home)
	pairing, client, err := Discover(ctx, home, computer.endpoint(), ssh.FingerprintSHA256(computer.hostSigner.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	client.Close()

	client, err = Connect(ctx, home, pairing)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()

	// A replaced volume reports a different machine.
	computer.identity.Store(`{"machine_id":"1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"}`)
	if _, err := Connect(ctx, home, pairing); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("expected identity mismatch, got %v", err)
	}
	computer.identity.Store(`{"machine_id":"` + testMachine + `"}`)

	// A different host key at the same address is refused.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	forged := pairing
	forged.HostKey = AuthorizedKey(otherSigner)
	if _, err := Connect(ctx, home, forged); err == nil {
		t.Fatal("connected despite a host key mismatch")
	}
}

func TestSaveLoadRemove(t *testing.T) {
	home := t.TempDir()
	if _, err := Load(home); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("expected not paired, got %v", err)
	}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(private)
	p := Pairing{Version: pairingVersion, MachineID: testMachine, Host: "vps.example.com", Port: 2222, User: DefaultUser, HostKey: AuthorizedKey(hostSigner), PairedAt: time.Unix(1, 0).UTC()}
	if err := Save(home, p); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(home)
	if err != nil || loaded != p {
		t.Fatalf("round trip changed the record: %+v %v", loaded, err)
	}
	for _, name := range []string{"pairing.json", "known_hosts"} {
		info, err := os.Stat(filepath.Join(Dir(home), name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s is not private", name)
		}
	}
	known, _ := os.ReadFile(filepath.Join(Dir(home), "known_hosts"))
	if !strings.HasPrefix(string(known), "[vps.example.com]:2222 ssh-ed25519 ") {
		t.Fatalf("unexpected known_hosts entry %q", known)
	}
	if err := Remove(home); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); !errors.Is(err, ErrNotPaired) {
		t.Fatal("record survived removal")
	}
}

func TestValidateRejectsUnsafeRecords(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(private)
	good := Pairing{Version: pairingVersion, MachineID: testMachine, Host: "10.0.0.5", Port: 22, User: DefaultUser, HostKey: AuthorizedKey(hostSigner)}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Pairing){
		"version":     func(p *Pairing) { p.Version = 2 },
		"machine":     func(p *Pairing) { p.MachineID = "vps-1" },
		"host option": func(p *Pairing) { p.Host = "-oProxyCommand=x" },
		"host space":  func(p *Pairing) { p.Host = "a b" },
		"host user":   func(p *Pairing) { p.Host = "root@host" },
		"port":        func(p *Pairing) { p.Port = 0 },
		"user":        func(p *Pairing) { p.User = "Root" },
		"host key":    func(p *Pairing) { p.HostKey = "not a key" },
		"two keys":    func(p *Pairing) { p.HostKey = good.HostKey + "\n" + good.HostKey },
	} {
		p := good
		mutate(&p)
		if p.Validate() == nil {
			t.Fatalf("accepted unsafe record: %s", name)
		}
	}
}
