package localssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

func linkLeaseClient(t *testing.T, configure ...func(*gliderssh.Server)) *ssh.Client {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	server := &gliderssh.Server{Handler: func(s gliderssh.Session) {
		if s.RawCommand() == "peer" {
			io.WriteString(s, "peer survived")
			s.Exit(0)
		} else {
			io.Copy(io.Discard, s)
			s.Exit(1)
		}
	}}
	for _, configure := range configure {
		configure(server)
	}
	server.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "fixture", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func assertLinkPeerAlive(t *testing.T, client *ssh.Client) {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal("peer connection closed", err)
	}
	defer session.Close()
	out, err := session.Output("peer")
	if err != nil || string(out) != "peer survived" {
		t.Fatalf("peer failed: %q %v", out, err)
	}
}

func TestBorrowedLinkCancellationDoesNotClosePeerConnection(t *testing.T) {
	client := linkLeaseClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	setup, cancelSetup := context.WithCancel(ctx)
	lease := newLinkSSHLease(ctx, setup, client, false)
	cancelSetup()
	cancel()
	lease.close()
	assertLinkPeerAlive(t, client)
}

func TestFailedSharedLinkSetupDoesNotRedialOrClosePeer(t *testing.T) {
	client := linkLeaseClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	calls := 0
	config := LinkConfig{RuntimeID: "owned", Directory: t.TempDir(), VerifiedOwner: "owner", SharedSSH: func(context.Context) (*ssh.Client, error) { calls++; return client, nil }}
	// A nil API ensures the shared path cannot silently repeat authentication.
	err := linkOnce(ctx, nil, config, "aabbccdd", "fixture.local", "unused", "owner", func(string, string) {})
	if err == nil || calls != 1 {
		t.Fatal("expected failed pairing over one borrowed connection", err, calls)
	}
	assertLinkPeerAlive(t, client)
}

func TestOwnedLinkCleanupStillClosesItsConnection(t *testing.T) {
	client := linkLeaseClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lease := newLinkSSHLease(ctx, ctx, client, true)
	lease.close()
	if session, err := client.NewSession(); err == nil {
		session.Close()
		t.Fatal("owned connection leaked")
	}
}

func TestBorrowedRelayFailureDoesNotCloseResidentConnection(t *testing.T) {
	client := linkLeaseClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// The fixture rejects forwarding. This exercises the actual relay cleanup,
	// not just the lease wrapper, and must leave the resident peer operational.
	if err := serveRelay(ctx, client, 1, "127.0.0.1:1", false); err == nil {
		t.Fatal("unavailable forwarding unexpectedly succeeded")
	}
	assertLinkPeerAlive(t, client)
}

func TestBorrowedRelayCancellationReleasesBridgesAndPreservesPeer(t *testing.T) {
	bridges, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer bridges.Close()
	client := linkLeaseClient(t, func(server *gliderssh.Server) {
		server.LocalPortForwardingCallback = func(gliderssh.Context, string, uint32) bool { return true }
		server.ChannelHandlers = map[string]gliderssh.ChannelHandler{"direct-tcpip": gliderssh.DirectTCPIPHandler, "session": gliderssh.DefaultSessionHandler}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveRelay(ctx, client, bridges.Addr().(*net.TCPAddr).Port, "127.0.0.1:1", false) }()
	bridges.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second))
	remote, err := bridges.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay cancellation stuck")
	}
	remote.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, err := remote.Read(one[:]); err != io.EOF {
		t.Fatal("idle bridge survived cancellation", err)
	}
	assertLinkPeerAlive(t, client)
}

func TestConnectorSessionCancellationDoesNotCloseSharedSSH(t *testing.T) {
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	client := linkLeaseClient(t, func(server *gliderssh.Server) {
		server.ChannelHandlers = map[string]gliderssh.ChannelHandler{"session": func(server *gliderssh.Server, conn *ssh.ServerConn, channel ssh.NewChannel, ctx gliderssh.Context) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-gate
			gliderssh.DefaultSessionHandler(server, conn, channel, ctx)
		}}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		session, err := connectorSession(ctx, client)
		if session != nil {
			session.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(gate)
		t.Fatal("channel open missing")
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			close(gate)
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(gate)
		t.Fatal("canceled channel open stuck")
	}
	close(gate)
	assertLinkPeerAlive(t, client)
}

func TestSerializedLinkCannotClaimVerifiedOwnership(t *testing.T) {
	var config LinkConfig
	if err := json.Unmarshal([]byte(`{"runtime_id":"other","VerifiedOwner":"owner","verified_owner":"owner","SharedSSH":{}}`), &config); err != nil {
		t.Fatal(err)
	}
	if config.VerifiedOwner != "" || config.SharedSSH != nil {
		t.Fatal("serialized owner bypass")
	}
}
