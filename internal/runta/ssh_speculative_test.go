package runta

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestCachedSSHRequiresExactPinnedHost(t *testing.T) {
	directory := t.TempDir()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "runtime_key"), pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	key := testSSHConfig(t).HostKey
	for _, host := range []string{"ssh.example.com", "other.example.com", "*"} {
		entry := append([]byte(host+" "), ssh.MarshalAuthorizedKey(key)...)
		if err := os.WriteFile(filepath.Join(directory, "known_hosts"), entry, 0600); err != nil {
			t.Fatal(err)
		}
		config := cachedSSHConfig("https://api.example.com", "owned-runtime", directory)
		if (config != nil) != (host == "ssh.example.com") {
			t.Fatalf("incorrect cached pin acceptance: %s", host)
		}
	}
}

type trackedSSHConn struct {
	ssh.Conn
	closed atomic.Bool
}

func (c *trackedSSHConn) Close() error { c.closed.Store(true); return nil }

func testSSHConfig(t *testing.T) *SSHConnectionConfig {
	t.Helper()
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return &SSHConnectionConfig{Address: "gateway:22", Config: &ssh.ClientConfig{User: "owned-runtime"}, HostKey: public, ClientKey: public}
}

func TestSSHHandshakeOverlapsButCannotBypassValidation(t *testing.T) {
	config := testSSHConfig(t)
	conn := &trackedSSHConn{}
	client := &ssh.Client{Conn: conn}
	dialed, validated := make(chan struct{}), make(chan struct{})
	finished := make(chan *ssh.Client, 1)
	go func() {
		got, err := connectPreparedSSH(context.Background(), config, func(context.Context) (*SSHConnectionConfig, error) {
			<-dialed
			<-validated
			return config, nil
		}, func(context.Context, *SSHConnectionConfig) (*ssh.Client, error) { close(dialed); return client, nil })
		if err != nil {
			t.Error(err)
		}
		finished <- got
	}()
	<-dialed
	select {
	case <-finished:
		t.Fatal("connection escaped before API validation")
	default:
	}
	close(validated)
	if got := <-finished; got != client || conn.closed.Load() {
		t.Fatal("validated connection was not reused")
	}
}

func TestChangedSSHIdentityDiscardsSpeculation(t *testing.T) {
	for _, field := range []string{"host", "client", "runtime", "address", "rejected"} {
		t.Run(field, func(t *testing.T) {
			cached, fresh := testSSHConfig(t), testSSHConfig(t)
			fresh.HostKey, fresh.ClientKey = cached.HostKey, cached.ClientKey
			switch field {
			case "host":
				fresh.HostKey = testSSHConfig(t).HostKey
			case "client":
				fresh.ClientKey = testSSHConfig(t).ClientKey
			case "runtime":
				fresh.Config.User = "another-runtime"
			case "address":
				fresh.Address = "another-gateway:22"
			}
			old := &trackedSSHConn{}
			dialed := make(chan struct{})
			calls := 0
			denied := errors.New("API validation failed")
			result, err := connectPreparedSSH(context.Background(), cached, func(context.Context) (*SSHConnectionConfig, error) {
				<-dialed
				if field == "rejected" {
					return nil, denied
				}
				return fresh, nil
			}, func(_ context.Context, config *SSHConnectionConfig) (*ssh.Client, error) {
				calls++
				if calls == 1 {
					close(dialed)
					return &ssh.Client{Conn: old}, nil
				}
				if !old.closed.Load() || config != fresh {
					t.Error("stale connection retained during fallback")
				}
				return &ssh.Client{Conn: &trackedSSHConn{}}, nil
			})
			if !old.closed.Load() {
				t.Fatal("unvalidated connection leaked")
			}
			if field == "rejected" {
				if !errors.Is(err, denied) || calls != 1 || result != nil {
					t.Fatal("failed authorization retried or escaped")
				}
			} else if err != nil || calls != 2 || result == nil {
				t.Fatalf("fresh validated connection missing: %v", err)
			}
		})
	}
}
