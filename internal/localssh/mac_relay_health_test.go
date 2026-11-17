package localssh

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fennec/internal/guardian"
	"fennec/internal/sandbox"
	glider "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

// Pace only the data connection after setup. A megabyte then remains queued
// beyond the old five-second timeout; the control connection remains idle.
type pacedRelayConn struct {
	net.Conn
	enabled *atomic.Bool
	closed  chan struct{}
	once    sync.Once
}

func (c *pacedRelayConn) Close() error { c.once.Do(func() { close(c.closed) }); return c.Conn.Close() }
func (c *pacedRelayConn) Write(p []byte) (int, error) {
	if !c.enabled.Load() {
		return c.Conn.Write(p)
	}
	total := 0
	for len(p) > 0 {
		timer := time.NewTimer(16 * time.Millisecond)
		select {
		case <-timer.C:
		case <-c.closed:
			timer.Stop()
			return total, net.ErrClosed
		}
		n, err := c.Conn.Write(p[:min(len(p), 1024)])
		total += n
		p = p[n:]
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func TestMacRelayHealthSurvivesQueuedBulkTransfer(t *testing.T) {
	h, _, _ := fullReverseHarness(t)
	options := h.options()
	dial := options.Dial
	var count atomic.Int32
	var pacing atomic.Bool
	options.Dial = func(ctx context.Context) (net.Conn, error) {
		c, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		if count.Add(1) == 1 {
			return &pacedRelayConn{Conn: c, enabled: &pacing, closed: make(chan struct{})}, nil
		}
		return c, nil
	}
	client := h.client(t, options)
	listener := remoteListen(t, client)
	tcp, stream := forwardedPair(t, listener)
	tcp.SetDeadline(time.Now().Add(40 * time.Second))
	pacing.Store(true)
	payload := bytes.Repeat([]byte("bulk-data-health"), 70000)
	done := make(chan error, 1)
	go func() { _, err := stream.Write(payload); stream.Close(); done <- err }()
	got, err := io.ReadAll(tcp)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("healthy queued transfer was closed: %d/%d bytes, %v", len(got), len(payload), err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if count.Load() != 2 {
		t.Fatalf("steady health reconnected: %d inner connections", count.Load())
	}
}

func TestMacRelayHealthSurvivesGlobalApprovalWait(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	broker := brokerFor(sandbox.PresetAuto, &guardian.Reviewer{Home: t.TempDir(), Respond: func(ctx context.Context, _ string, _ string, _ string) (string, error) {
		close(started)
		select {
		case <-release:
			return `{"outcome":"allow"}`, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}, io.Discard)
	h, _, _ := reverseHarness(t, broker)
	client := h.client(t, h.options())
	result := make(chan error, 1)
	go func() {
		l, err := client.Listen("tcp", "127.0.0.1:0")
		if l != nil {
			l.Close()
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("approval did not start")
	}
	select {
	case err := <-result:
		t.Fatalf("approval ended before decision: %v", err)
	case <-time.After(11 * time.Second):
	}
	once.Do(func() { close(release) })
	select {
	case err := <-result:
		if err != nil {
			t.Fatal("heartbeat killed pending approval", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approved forward did not finish")
	}
}

func TestConnectionHealthBindsSessionAndIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := connectionHealth(ctx, func(glider.PublicKey) bool { return true })
	server := &glider.Server{PublicKeyHandler: func(glider.Context, glider.PublicKey) bool { return true }, RequestHandlers: map[string]glider.RequestHandler{registerConnectionHealth: handler, probeConnectionHealth: handler}}
	host := newSigner(t)
	server.AddHostKey(host)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	identity := newSigner(t)
	connect := func(key ssh.Signer) *ssh.Client {
		c, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "fennec", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	data, control, other := connect(identity), connect(identity), connect(newSigner(t))
	if ok, _, err := data.SendRequest(registerConnectionHealth, true, nil); err != nil || !ok {
		t.Fatal("register", ok, err)
	}
	payload := ssh.Marshal(struct{ Session []byte }{data.SessionID()})
	if ok, _, err := control.SendRequest(probeConnectionHealth, true, payload); err != nil || !ok {
		t.Fatal("same device could not probe", ok, err)
	}
	if ok, _, err := other.SendRequest(probeConnectionHealth, true, payload); err != nil || ok {
		t.Fatal("cross-device session lookup", ok, err)
	}
	for _, bad := range [][]byte{nil, []byte("invalid"), ssh.Marshal(struct{ Session []byte }{[]byte("unknown")})} {
		if ok, _, err := control.SendRequest(probeConnectionHealth, true, bad); err != nil || ok {
			t.Fatal("invalid lookup", ok, err)
		}
	}
	data.Close()
	deadline := time.Now().Add(time.Second)
	for {
		ok, _, err := control.SendRequest(probeConnectionHealth, true, payload)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed data connection remained registered")
		}
		time.Sleep(time.Millisecond)
	}
}

// Silently lose outgoing bytes without returning a socket error, as a
// blackholed peer would. The watchdog must remain bounded in this case.
type blackholeRelayConn struct {
	net.Conn
	stalled *atomic.Bool
}

func (c *blackholeRelayConn) Write(p []byte) (int, error) {
	if c.stalled.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func TestMacRelayHealthClosesTrulyUnresponsivePeer(t *testing.T) {
	h, _, _ := fullReverseHarness(t)
	options := h.options()
	dial := options.Dial
	var stalled atomic.Bool
	var count atomic.Int32
	options.Dial = func(ctx context.Context) (net.Conn, error) {
		if stalled.Load() {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		conn, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		if count.Add(1) == 2 {
			return &blackholeRelayConn{conn, &stalled}, nil
		}
		return conn, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := &macUpstream{ctx: ctx, options: options, pool: make(chan pooledSession, relayPoolSize)}
	defer u.close()
	client, err := u.dial()
	if err != nil {
		t.Fatal(err)
	}
	u.client = client
	stalled.Store(true)
	go u.watch(client)
	ended := make(chan error, 1)
	go func() { ended <- client.Wait() }()
	select {
	case <-ended:
	case <-time.After(17 * time.Second):
		t.Fatal("unresponsive peer survived interval + two five-second probe attempts")
	}
}

func TestMacRelayHealthRecoversControlWithoutReplacingData(t *testing.T) {
	h, _, _ := fullReverseHarness(t)
	u := &macUpstream{ctx: context.Background(), options: h.options(), pool: make(chan pooledSession, relayPoolSize)}
	defer u.close()
	data, err := u.dial()
	if err != nil {
		t.Fatal(err)
	}
	u.client = data
	value, _ := u.health.Load(data)
	value.(*macRelayHealth).client.Close()
	if !u.probeHealth(data, make(chan struct{})) {
		t.Fatal("isolated control failure retired healthy data")
	}
	if u.client != data {
		t.Fatal("data generation replaced")
	}
	channel, reqs, err := data.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	go ssh.DiscardRequests(reqs)
}

func TestMacRelayHealthConnectionVerifiesHostKey(t *testing.T) {
	trusted := newMacServer(t, 0)
	wrong := newMacServer(t, 0)
	options := trusted.options()
	dial := options.Dial
	var count atomic.Int32
	options.Dial = func(ctx context.Context) (net.Conn, error) {
		if count.Add(1) == 2 {
			return (&net.Dialer{}).DialContext(ctx, "tcp", wrong.mac)
		}
		return dial(ctx)
	}
	u := &macUpstream{ctx: context.Background(), options: options, pool: make(chan pooledSession, relayPoolSize)}
	defer u.close()
	if c, err := u.dial(); err == nil {
		c.Close()
		t.Fatal("control connection accepted a different host identity")
	}
}
