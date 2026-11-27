package tailnet

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeSSH serves one in-process SSH server per dial: it accepts the password
// "secret" or clientKey, and runs exec requests with sh in home.
func fakeSSH(t *testing.T, home string, clientKey ssh.PublicKey) {
	t.Helper()
	_, hostPrivate, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostPrivate)
	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) == "secret" {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if clientKey != nil && bytes.Equal(key.Marshal(), clientKey.Marshal()) {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	config.AddHostKey(hostSigner)
	old := dial
	t.Cleanup(func() { dial = old })
	dial = func(ctx context.Context, p paths, host string, port int) (net.Conn, error) {
		// A loopback socket, not net.Pipe: both SSH sides write their
		// version line first, which deadlocks on an unbuffered pipe.
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		defer listener.Close()
		client, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			return nil, err
		}
		server, err := listener.Accept()
		if err != nil {
			return nil, err
		}
		go func() {
			conn, channels, requests, err := ssh.NewServerConn(server, config)
			if err != nil {
				server.Close()
				return
			}
			defer conn.Close()
			go ssh.DiscardRequests(requests)
			for incoming := range channels {
				channel, reqs, _ := incoming.Accept()
				for req := range reqs {
					if req.Type != "exec" {
						req.Reply(false, nil)
						continue
					}
					var payload struct{ Command string }
					ssh.Unmarshal(req.Payload, &payload)
					req.Reply(true, nil)
					command := exec.Command("sh", "-c", payload.Command)
					command.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
					command.Stdout, command.Stderr = channel, channel.Stderr()
					status := 0
					if command.Run() != nil {
						status = 1
					}
					channel.CloseWrite()
					channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
					channel.Close()
				}
			}
		}()
		return client, nil
	}
}

func TestAuthorizeWithPasswordAndKey(t *testing.T) {
	cloud, target := t.TempDir(), t.TempDir()
	p := paths{cloud}
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	clientPublic, _ := ssh.NewPublicKey(public)
	fakeSSH(t, target, clientPublic)
	ctx := context.Background()

	if code := authorize(ctx, p, "100.64.0.9", 22, Request{User: "me", Password: "wrong"}, false); code != ErrAuthFailed {
		t.Fatalf("wrong password: %q", code)
	}
	if code := authorize(ctx, p, "100.64.0.9", 22, Request{User: "me", PrivateKey: "not a key"}, false); code != ErrKeyInvalid {
		t.Fatalf("bad key: %q", code)
	}
	block, _ := ssh.MarshalPrivateKeyWithPassphrase(private, "", []byte("pass"))
	encrypted := string(pem.EncodeToMemory(block))
	if code := authorize(ctx, p, "100.64.0.9", 22, Request{User: "me", PrivateKey: encrypted}, false); code != ErrKeyPassphrase {
		t.Fatalf("encrypted key without passphrase: %q", code)
	}
	if code := authorize(ctx, p, "100.64.0.9", 22, Request{User: "me", PrivateKey: encrypted, Passphrase: "nope"}, false); code != ErrKeyPassphrase {
		t.Fatalf("wrong passphrase: %q", code)
	}

	if code := authorize(ctx, p, "100.64.0.9", 22, Request{User: "me", Password: "secret"}, false); code != "" {
		t.Fatalf("password: %q", code)
	}
	key, _ := os.ReadFile(p.key() + ".pub")
	data, err := os.ReadFile(filepath.Join(target, ".ssh", "authorized_keys"))
	if err != nil || !strings.Contains(string(data), strings.Fields(string(key))[1]) || !strings.Contains(string(data), KeyOptions) {
		t.Fatalf("authorized_keys = %q, %v", data, err)
	}
	if code := authorize(ctx, p, "100.64.0.9", 22, Request{User: "me", PrivateKey: encrypted, Passphrase: "pass"}, false); code != "" {
		t.Fatalf("key with passphrase: %q", code)
	}
	data, _ = os.ReadFile(filepath.Join(target, ".ssh", "authorized_keys"))
	if strings.Count(string(data), KeyComment) != 1 {
		t.Fatalf("authorized twice: %q", data)
	}
	known, _ := os.ReadFile(p.knownHosts())
	if !strings.HasPrefix(string(known), "100.64.0.9 ") {
		t.Fatalf("known_hosts = %q", known)
	}
}

func TestAuthorizeRejectsChangedHostKey(t *testing.T) {
	cloud, target := t.TempDir(), t.TempDir()
	p := paths{cloud}
	fakeSSH(t, target, nil)
	os.MkdirAll(p.sshDir(), 0700)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	otherKey, _ := ssh.NewPublicKey(other)
	os.WriteFile(p.knownHosts(), []byte("[100.64.0.9]:2222 "+string(ssh.MarshalAuthorizedKey(otherKey))), 0600)
	if code := authorize(context.Background(), p, "100.64.0.9", 2222, Request{User: "me", Password: "secret"}, false); code != ErrHostKeyChange {
		t.Fatalf("changed host key: %q", code)
	}
}

func TestRequestValidateSecrets(t *testing.T) {
	ok := Request{ID: "n1", User: "me", Port: 2222, Password: "x"}
	if ok.Validate(true) != nil {
		t.Fatal("valid request rejected")
	}
	for _, r := range []Request{{ID: "n1", User: "me", Port: 70000}, {ID: "n1", User: "me", Password: strings.Repeat("x", 2000)}} {
		if r.Validate(true) == nil {
			t.Fatalf("accepted %+v", r.Port)
		}
	}
}

func TestEncodePowerShell(t *testing.T) {
	if got := encodePowerShell("hi"); got != "aABpAA==" {
		t.Fatal(got)
	}
}

// fakeTailscaleSSH serves a no-credentials SSH server like Tailscale SSH: it
// accepts the sign-in, or with check it sends the approval banner and holds.
func fakeTailscaleSSH(t *testing.T, check bool) {
	t.Helper()
	_, hostPrivate, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostPrivate)
	config := &ssh.ServerConfig{NoClientAuth: true}
	if check {
		config.BannerCallback = func(ssh.ConnMetadata) string {
			return "# Tailscale SSH requires an additional check.\n# To authenticate, visit: https://login.tailscale.com/a/abc123\n"
		}
		config.NoClientAuthCallback = func(ssh.ConnMetadata) (*ssh.Permissions, error) {
			time.Sleep(5 * time.Second)
			return nil, os.ErrPermission
		}
	}
	config.AddHostKey(hostSigner)
	old := dial
	t.Cleanup(func() { dial = old })
	dial = func(ctx context.Context, p paths, host string, port int) (net.Conn, error) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		defer listener.Close()
		client, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			return nil, err
		}
		server, err := listener.Accept()
		if err != nil {
			return nil, err
		}
		go func() {
			conn, channels, requests, err := ssh.NewServerConn(server, config)
			if err != nil {
				server.Close()
				return
			}
			go ssh.DiscardRequests(requests)
			go func() {
				for c := range channels {
					c.Reject(ssh.Prohibited, "")
				}
			}()
			conn.Wait()
		}()
		return client, nil
	}
}

func TestTailscaleSSHCheck(t *testing.T) {
	p := paths{t.TempDir()}
	fakeTailscaleSSH(t, false)
	if code, url := tailscaleSSHCheck(context.Background(), p, "100.64.0.9", "me"); code != "" || url != "" {
		t.Fatalf("accept: %q %q", code, url)
	}
	fakeTailscaleSSH(t, true)
	start := time.Now()
	code, url := tailscaleSSHCheck(context.Background(), paths{t.TempDir()}, "100.64.0.9", "me")
	if code != ErrSSHCheck || url != "https://login.tailscale.com/a/abc123" {
		t.Fatalf("check: %q %q", code, url)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("check held the sign-in for %v", time.Since(start))
	}
}

func TestDeviceTailscaleSSH(t *testing.T) {
	var n node
	json.Unmarshal([]byte(`{"ID":"n1","HostName":"box","sshHostKeys":["ssh-ed25519 AAAA"]}`), &n)
	if !n.device().TailscaleSSH {
		t.Fatal("sshHostKeys not read")
	}
}
