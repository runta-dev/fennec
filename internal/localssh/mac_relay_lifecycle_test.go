package localssh

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func awaitRelayExit(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled relay is still serving its SSH connection")
	}
}

func TestMacRelayCancellationClosesConnection(t *testing.T) {
	for _, phase := range []string{"handshake", "dial", "idle", "command"} {
		t.Run(phase, func(t *testing.T) {
			h := newMacServer(t, 0)
			local, relay := socketPair(t)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			options := h.options()
			if phase == "dial" {
				options.Dial = func(ctx context.Context) (net.Conn, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}
			}
			go func() { RunMacRelay(ctx, relay, options); close(done) }()
			t.Cleanup(func() { cancel(); local.Close(); relay.Close(); awaitRelayExit(t, done) })
			if phase != "handshake" {
				connection, channels, requests, err := gossh.NewClientConn(local, "relay", &gossh.ClientConfig{
					User: "fennec", Auth: []gossh.AuthMethod{gossh.PublicKeys(h.identity)},
					HostKeyCallback: gossh.FixedHostKey(h.relayKey.PublicKey()),
				})
				if err != nil {
					t.Fatal(err)
				}
				client := gossh.NewClient(connection, channels, requests)
				t.Cleanup(func() { client.Close() })
				if phase == "command" {
					session, err := client.NewSession()
					if err != nil {
						t.Fatal(err)
					}
					defer session.Close()
					stdin, err := session.StdinPipe()
					if err != nil {
						t.Fatal(err)
					}
					defer stdin.Close()
					if err := session.Start("cat"); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "idle" {
					if _, _, err := run(t, client, "true", ""); err != nil {
						t.Fatal(err)
					}
				}
				clientDone := make(chan struct{})
				go func() { client.Wait(); close(clientDone) }()
				cancel()
				awaitRelayExit(t, clientDone)
			} else {
				cancel()
			}
			awaitRelayExit(t, done)
		})
	}
}

func TestMacRelayRecoversAfterMacReturns(t *testing.T) {
	h := newMacServer(t, 0)
	options := h.options()
	dial := options.Dial
	var online atomic.Bool
	options.Dial = func(ctx context.Context) (net.Conn, error) {
		if !online.Load() {
			return nil, errors.New("connection refused")
		}
		return dial(ctx)
	}
	client := h.client(t, options)
	if _, _, err := run(t, client, "true", ""); err == nil {
		t.Fatal("offline command succeeded")
	}
	online.Store(true)
	if out, _, err := run(t, client, "after reconnect", ""); err != nil || out != "ran after reconnect\n" {
		t.Fatal(out, err)
	}
}

func TestMacRelayReplacesDisconnectedUpstream(t *testing.T) {
	h := newMacServer(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := &macUpstream{ctx: ctx, options: h.options(), pool: make(chan pooledSession, relayPoolSize)}
	defer u.close()
	first, err := u.connect()
	if err != nil {
		t.Fatal(err)
	}
	waitFor := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !condition() {
			if time.Now().After(deadline) {
				t.Fatal("upstream did not settle")
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitFor(func() bool { return len(u.pool) == relayPoolSize })
	first.Close()
	waitFor(func() bool { u.mu.Lock(); defer u.mu.Unlock(); return u.client == nil })
	// Old pooled channels still exist. The next session must discard them
	// and use a new authenticated connection, not inherit their closed state.
	channel, requests, err := u.session()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	go gossh.DiscardRequests(requests)
	ok, err := channel.SendRequest("exec", true, gossh.Marshal(struct{ Command string }{"after disconnect"}))
	if err != nil || !ok {
		t.Fatal("command on recovered connection", ok, err)
	}
	output, err := io.ReadAll(channel)
	if err != nil || string(output) != "ran after disconnect\n" {
		t.Fatal(string(output), err)
	}
}

func TestRelayStdioCloseUnblocksRead(t *testing.T) {
	input, writer := io.Pipe()
	reader, output := io.Pipe()
	defer input.Close()
	defer writer.Close()
	defer output.Close()
	defer reader.Close()
	conn := stdioConn{Reader: input, WriteCloser: output}
	done := make(chan struct{})
	go func() { var b [1]byte; conn.Read(b[:]); close(done) }()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	awaitRelayExit(t, done)
}
