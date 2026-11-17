package localssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"fennec/internal/sandbox"
	"golang.org/x/crypto/ssh"
)

// Exercise the real connector registration, broker, relay and SSH client,
// rather than a mock that could accidentally omit a production handler.
func reverseHarness(t *testing.T, broker *Broker) (*relayHarness, func(), func()) {
	t.Helper()
	dir := t.TempDir()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(keyPath, ssh.MarshalAuthorizedKey(identity.PublicKey()), 0600); err != nil {
		t.Fatal(err)
	}
	input, writer := io.Pipe()
	events := &lockedBuffer{}
	broker.output = events
	broker.directory = dir
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunWithApproval(ctx, dir, input, events, true, broker) }()
	t.Cleanup(func() {
		cancel()
		writer.Close()
		input.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("connector did not stop")
		}
	})
	var address string
	deadline := time.Now().Add(5 * time.Second)
	for address == "" && time.Now().Before(deadline) {
		for _, line := range strings.Split(events.String(), "\n") {
			var e Event
			if json.Unmarshal([]byte(line), &e) == nil && e.Type == "listening" {
				address = e.Address
			}
		}
		time.Sleep(time.Millisecond)
	}
	if address == "" {
		t.Fatal("connector did not listen")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "host_key"))
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	h := &relayHarness{mac: address, macKey: host, identity: identity, identityPEM: pem.EncodeToMemory(block), relayKey: newSigner(t)}
	return h, func() {
		if err := os.WriteFile(keyPath, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}, cancel
}

func fullReverseHarness(t *testing.T) (*relayHarness, func(), func()) {
	return reverseHarness(t, brokerFor(sandbox.PresetFull, nil, io.Discard))
}

func remoteListen(t *testing.T, c *ssh.Client) net.Listener {
	t.Helper()
	l, err := c.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func forwardedPair(t *testing.T, l net.Listener) (*net.TCPConn, net.Conn) {
	t.Helper()
	raw, err := net.DialTimeout("tcp", l.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tcp := raw.(*net.TCPConn)
	tcp.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { tcp.Close() })
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); accepted <- c }()
	select {
	case c := <-accepted:
		if c == nil {
			t.Fatal("forward rejected")
		}
		t.Cleanup(func() { c.Close() })
		return tcp, c
	case <-time.After(5 * time.Second):
		t.Fatal("forward was not delivered")
	}
	return nil, nil
}

func waitPortClosed(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return
		}
		c.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("listener remained open:", addr)
}

func TestReverseForwardRoundTripCancelAndOwnership(t *testing.T) {
	h, _, _ := fullReverseHarness(t)
	client := h.client(t, h.options())
	listener := remoteListen(t, client)
	other := h.client(t, h.options())
	addr := listener.Addr().(*net.TCPAddr)
	bind := reverseBind{addr.IP.String(), uint32(addr.Port)}
	if ok, _, err := other.SendRequest("cancel-tcpip-forward", true, ssh.Marshal(bind)); err != nil || ok {
		t.Fatalf("another connection canceled listener: %v %v", ok, err)
	}
	if ok, _, err := client.SendRequest("tcpip-forward", true, ssh.Marshal(bind)); err != nil || ok {
		t.Fatalf("duplicate listener accepted: %v %v", ok, err)
	}
	tcp, stream := forwardedPair(t, listener)
	payload := bytes.Repeat([]byte("reverse-forward\x00"), 40000)
	done := make(chan error, 1)
	go func() {
		_, err := tcp.Write(payload)
		if err == nil {
			err = tcp.CloseWrite()
		}
		done <- err
	}()
	received, err := io.ReadAll(stream)
	if err != nil || !bytes.Equal(received, payload) {
		t.Fatalf("large upload/half close: bytes=%d err=%v", len(received), err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Cancellation closes only the listener; established streams still reply.
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	waitPortClosed(t, listener.Addr().String())
	if _, err := stream.Write([]byte("response after EOF")); err != nil {
		t.Fatal(err)
	}
	stream.Close()
	reply, err := io.ReadAll(tcp)
	if err != nil || string(reply) != "response after EOF" {
		t.Fatalf("half-close response: %q %v", reply, err)
	}
	// Rebinding the allocated port proves dynamic-port cancellation used the
	// assigned port rather than leaving a leaked listener under port zero.
	next, err := other.Listen("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}

func TestReverseForwardLifecycle(t *testing.T) {
	for _, mode := range []string{"client disconnect", "pairing revoked", "connector stopped"} {
		t.Run(mode, func(t *testing.T) {
			h, revoke, stop := fullReverseHarness(t)
			client := h.client(t, h.options())
			listener := remoteListen(t, client)
			tcp, stream := forwardedPair(t, listener)
			io.WriteString(tcp, "a")
			var b [1]byte
			if _, err := io.ReadFull(stream, b[:]); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "client disconnect":
				client.Close()
			case "pairing revoked":
				revoke()
			case "connector stopped":
				stop()
			}
			waitPortClosed(t, listener.Addr().String())
			tcp.SetReadDeadline(time.Now().Add(4 * time.Second))
			if _, err := tcp.Read(b[:]); err == nil {
				t.Fatal("active stream survived cancellation")
			} else if e, ok := err.(net.Error); ok && e.Timeout() {
				t.Fatal("active stream leaked")
			}
			done := make(chan error, 1)
			go func() { done <- client.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("relay silently kept lost remote forwarding alive")
			}
		})
	}
}

func TestReverseForwardApprovalAndValidation(t *testing.T) {
	for _, outcome := range []string{"allow", "deny"} {
		t.Run(outcome, func(t *testing.T) {
			broker := brokerFor(sandbox.PresetAuto, reviewer(t, `{"outcome":"`+outcome+`","rationale":"test policy"}`, false), io.Discard)
			h, _, _ := reverseHarness(t, broker)
			client := h.client(t, h.options())
			listener, err := client.Listen("tcp", "127.0.0.1:0")
			if outcome == "deny" {
				if err == nil {
					listener.Close()
					t.Fatal("denied reverse forward succeeded")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				listener.Close()
			}
		})
	}
	h, _, _ := fullReverseHarness(t)
	client := h.client(t, h.options())
	for _, payload := range [][]byte{[]byte("bad"), ssh.Marshal(reverseBind{"127.0.0.1", 65536}), ssh.Marshal(reverseBind{"host\nname", 0}), ssh.Marshal(reverseBind{"example.com", 0})} {
		if ok, _, err := client.SendRequest("tcpip-forward", true, payload); err != nil || ok {
			t.Fatalf("invalid request: %v %v", ok, err)
		}
	}
	listeners := make([]net.Listener, 0, 16)
	for i := 0; i < 16; i++ {
		listeners = append(listeners, remoteListen(t, client))
	}
	if l, err := client.Listen("tcp", "127.0.0.1:0"); err == nil {
		l.Close()
		t.Fatal("listener capacity not enforced")
	}
	for _, l := range listeners {
		l.Close()
	}
}

func TestReverseForwardOpenSSH(t *testing.T) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH not installed")
	}
	h, _, _ := fullReverseHarness(t)
	dir := t.TempDir()
	key := filepath.Join(dir, "identity")
	if err := os.WriteFile(key, h.identityPEM, 0600); err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(known, append([]byte("reverse-test "), ssh.MarshalAuthorizedKey(h.relayKey.PublicKey())...), 0600); err != nil {
		t.Fatal(err)
	}
	relay, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() { relay.Close() })
	go func() {
		for {
			conn, err := relay.Accept()
			if err != nil {
				return
			}
			go RunMacRelay(ctx, conn, h.options())
		}
	}()
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close() })
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); data, _ := io.ReadAll(c); c.Write(append([]byte("echo:"), data...)) }()
		}
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bind := reserved.Addr().String()
	reserved.Close()
	// Darwin's long per-test temp path exceeds sockaddr_un once OpenSSH adds
	// its temporary socket suffix. Keep only the control socket under /tmp.
	controlDir, err := os.MkdirTemp("/tmp", "fen-r-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(controlDir) })
	control := filepath.Join(controlDir, "control")
	forward := bind + ":" + target.Addr().String()
	args := []string{"-F", "/dev/null", "-i", key, "-p", strconv.Itoa(relay.Addr().(*net.TCPAddr).Port), "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "HostKeyAlias=reverse-test", "-o", "UserKnownHostsFile=" + known, "-o", "ExitOnForwardFailure=yes", "-M", "-S", control, "-NT", "-R", forward, "fennec@127.0.0.1"}
	cmd := exec.Command(sshPath, args...)
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("OpenSSH did not exit")
		}
	})
	var tcp *net.TCPConn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", bind, 100*time.Millisecond)
		if err == nil {
			tcp = c.(*net.TCPConn)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tcp == nil {
		t.Fatalf("OpenSSH -R did not listen: %s", stderr.String())
	}
	defer tcp.Close()
	tcp.SetDeadline(time.Now().Add(5 * time.Second))
	tcp.Write([]byte("openssh"))
	tcp.CloseWrite()
	data, err := io.ReadAll(tcp)
	if err != nil || string(data) != "echo:openssh" {
		t.Fatalf("OpenSSH roundtrip: %q %v; %s", data, err, stderr.String())
	}
	cancelCmd := exec.Command(sshPath, "-F", "/dev/null", "-S", control, "-O", "cancel", "-R", forward, "fennec@127.0.0.1")
	if out, err := cancelCmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenSSH cancellation: %s %v", out, err)
	}
	waitPortClosed(t, bind)
}
