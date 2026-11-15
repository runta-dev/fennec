package sshcarrier

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fennec/internal/connectivity"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

type fixture struct {
	socket        string
	signer        ssh.Signer
	dials         atomic.Int32
	commands      atomic.Int32
	channels      atomic.Int32
	probes        atomic.Int32
	blockChannels atomic.Bool
	blockRequests atomic.Bool
	holdHealth    atomic.Bool
	healthGate    chan struct{}
	channelGate   chan struct{}
	mu            sync.Mutex
	upstream      []*ssh.ServerConn
	streams       []*blackholeConn
	done          chan error
}

// Discard packets without closing TCP or delivering EOF. This models a lost
// route/NAT mapping rather than the clean disconnection covered by Close tests.
type blackholeConn struct {
	net.Conn
	drop atomic.Bool
}

func (c *blackholeConn) Read(p []byte) (int, error) {
	for {
		n, err := c.Conn.Read(p)
		if err != nil || !c.drop.Load() {
			return n, err
		}
	}
}

func (c *blackholeConn) Write(p []byte) (int, error) {
	if c.drop.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func newFixture(t *testing.T, idle time.Duration, policies ...healthPolicy) *fixture {
	t.Helper()
	f := &fixture{signer: testSigner(t), done: make(chan error, 1), channelGate: make(chan struct{}), healthGate: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	// Darwin UNIX paths are limited to 104 bytes, shorter than t.TempDir paths.
	directory, err := os.MkdirTemp("", "carrier-")
	if err != nil {
		t.Fatal(err)
	}
	f.socket = filepath.Join(directory, "test.sock")
	listener, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			raw, err := remote.Accept()
			if err != nil {
				return
			}
			go func() {
				config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
					if meta.User() != "runtime" || !bytes.Equal(key.Marshal(), f.signer.PublicKey().Marshal()) {
						return nil, fmt.Errorf("bad identity")
					}
					return nil, nil
				}}
				config.AddHostKey(f.signer)
				stream := &blackholeConn{Conn: raw}
				conn, channels, requests, err := ssh.NewServerConn(stream, config)
				if err != nil {
					raw.Close()
					return
				}
				defer conn.Close()
				f.mu.Lock()
				f.upstream = append(f.upstream, conn)
				f.streams = append(f.streams, stream)
				f.mu.Unlock()
				go func() {
					for r := range requests {
						if r.Type == "keepalive@openssh.com" {
							f.probes.Add(1)
						}
						r.Reply(r.Type == "probe", []byte("alive"))
					}
				}()
				for incoming := range channels {
					go func() {
						if f.blockChannels.Load() {
							select {
							case <-ctx.Done():
								return
							case <-f.channelGate:
							}
						}
						ch, requests, err := incoming.Accept()
						if err != nil {
							return
						}
						defer ch.Close()
						f.channels.Add(1)
						if incoming.ChannelType() == "direct-tcpip" {
							go ssh.DiscardRequests(requests)
							io.Copy(ch, ch)
							return
						}
						for request := range requests {
							if request.Type == "keepalive@openssh.com" {
								f.probes.Add(1)
							}
							if f.blockRequests.Load() {
								continue
							}
							if request.Type != "exec" {
								request.Reply(false, nil)
								continue
							}
							var command struct{ Command string }
							if ssh.Unmarshal(request.Payload, &command) != nil {
								return
							}
							request.Reply(true, nil)
							if command.Command == "exec /bin/cat" {
								// The echo service must require actual channel data;
								// global SSH acknowledgements alone cannot satisfy it.
								buffer := make([]byte, 32)
								for {
									if _, err := io.ReadFull(ch, buffer); err != nil {
										return
									}
									if f.blockRequests.Load() {
										continue
									}
									if f.holdHealth.Load() {
										select {
										case <-f.healthGate:
										case <-ctx.Done():
											return
										}
									}
									if _, err := ch.Write(buffer); err != nil {
										return
									}
									f.probes.Add(1)
								}
							}
							f.commands.Add(1)
							if command.Command == "read-stdin" {
								data, err := io.ReadAll(ch)
								if err != nil {
									return
								}
								ch.Write(data)
							} else {
								io.WriteString(ch, command.Command)
							}
							io.WriteString(ch.Stderr(), "diagnostic")
							// Intentionally put EOF before exit-status to exercise ordering.
							ch.CloseWrite()
							ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{7}))
							return
						}
					}()
				}
			}()
		}
	}()
	go func() {
		f.done <- serve(ctx, listener, f.signer, func(ctx context.Context) (*ssh.Client, error) {
			f.dials.Add(1)
			raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", remote.Addr().String())
			if err != nil {
				return nil, err
			}
			stop := context.AfterFunc(ctx, func() { raw.Close() })
			defer stop()
			conn, channels, requests, err := ssh.NewClientConn(raw, remote.Addr().String(), &ssh.ClientConfig{User: "runtime", Auth: []ssh.AuthMethod{ssh.PublicKeys(f.signer)}, HostKeyCallback: ssh.FixedHostKey(f.signer.PublicKey())})
			if err != nil {
				raw.Close()
				return nil, err
			}
			return ssh.NewClient(conn, channels, requests), nil
		}, idle, policies...)
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		remote.Close()
		select {
		case err := <-f.done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("carrier did not stop")
		}
		os.RemoveAll(directory)
	})
	return f
}

func TestReplacementUsesPreviouslyOpenedChannelWithoutRunningIdleCommands(t *testing.T) {
	f := newFixture(t, time.Minute)
	first := f.lease(t)
	if err := runCommand(first, "first"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for f.channels.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.channels.Load() != 3 || f.commands.Load() != 1 {
		t.Fatal("idle transport channels were not prepared independently", f.channels.Load(), f.commands.Load())
	}
	// A fresh channel open cannot finish until released. The replacement's
	// command must finish through an already confirmed channel instead.
	f.blockChannels.Store(true)
	defer close(f.channelGate)
	first.Close()
	replacement := f.lease(t)
	done := make(chan error, 1)
	go func() { done <- runCommand(replacement, "replacement") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement waited for a new WAN channel")
	}
	if f.commands.Load() != 2 || f.dials.Load() != 1 {
		t.Fatal("replacement repeated commands or authentication")
	}
}

func TestCarrierForwardsStdinEOFThroughRetainedChannels(t *testing.T) {
	f := newFixture(t, time.Minute)
	for i := 0; i < 3; i++ {
		client := f.lease(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		stop := context.AfterFunc(ctx, func() { client.Close() })
		session, err := client.NewSession()
		if err != nil {
			stop()
			cancel()
			client.Close()
			t.Fatal(err)
		}
		input := bytes.Repeat([]byte("pairing-script\n"), 4096)
		session.Stdin = bytes.NewReader(input)
		output, err := session.Output("read-stdin")
		stop()
		cancel()
		session.Close()
		client.Close()
		exit, ok := err.(*ssh.ExitError)
		if !ok || exit.ExitStatus() != 7 || !bytes.Equal(input, output) {
			t.Fatalf("lease %d lost stdin, EOF, or exit status: bytes=%d err=%v", i, len(output), err)
		}
	}
}

func (f *fixture) lease(t *testing.T) *ssh.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := dialLocal(ctx, f.socket, f.signer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func runCommand(client *ssh.Client, command string) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	var output, stderr bytes.Buffer
	session.Stdout, session.Stderr = &output, &stderr
	err = session.Run(command)
	status, ok := err.(*ssh.ExitError)
	if !ok || status.ExitStatus() != 7 || output.String() != command || stderr.String() != "diagnostic" {
		return fmt.Errorf("command: output=%q stderr=%q error=%v", output.String(), stderr.String(), err)
	}
	return nil
}

func TestDaemonReplacementRetainsOnlySSH(t *testing.T) {
	f := newFixture(t, time.Minute)
	first := f.lease(t)
	if err := runCommand(first, "before"); err != nil {
		t.Fatal(err)
	}
	first.Close()
	second := f.lease(t)
	if err := runCommand(second, "after"); err != nil {
		t.Fatal(err)
	}
	if f.dials.Load() != 1 || f.commands.Load() != 2 {
		t.Fatalf("dials=%d commands=%d", f.dials.Load(), f.commands.Load())
	}
}

func TestConcurrentLeasesCoalesceAndPreservePeer(t *testing.T) {
	f := newFixture(t, time.Minute)
	const count = 8
	clients := make([]*ssh.Client, count)
	for i := range clients {
		clients[i] = f.lease(t)
	}
	var workers sync.WaitGroup
	for i, client := range clients {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := runCommand(client, fmt.Sprint(i)); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	clients[0].Close()
	if err := runCommand(clients[1], "peer survives"); err != nil {
		t.Fatal(err)
	}
	if f.dials.Load() != 1 {
		t.Fatalf("%d upstream connections", f.dials.Load())
	}
}

func TestForwardingAndRemoteFailure(t *testing.T) {
	f := newFixture(t, time.Minute)
	client := f.lease(t)
	ok, reply, err := client.SendRequest("probe", true, nil)
	if err != nil || !ok || string(reply) != "alive" {
		t.Fatalf("global request: %v %s %v", ok, reply, err)
	}
	ch, requests, err := client.OpenChannel("direct-tcpip", []byte("opaque payload"))
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(requests)
	if _, err := ch.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 5)
	if _, err := io.ReadFull(ch, buffer); err != nil || string(buffer) != "hello" {
		t.Fatalf("forwarding: %s %v", buffer, err)
	}
	f.mu.Lock()
	f.upstream[0].Close()
	f.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WAN failure hidden by local connection")
	}
	replacement := f.lease(t)
	if err := runCommand(replacement, "new transport"); err != nil {
		t.Fatal(err)
	}
	if f.dials.Load() != 2 {
		t.Fatalf("%d connections", f.dials.Load())
	}
}

func TestBlackholedCarrierRetiresLeasesAndReconnects(t *testing.T) {
	f := newFixture(t, time.Minute)
	client := f.lease(t)
	if err := runCommand(client, "before outage"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.streams[0].drop.Store(true)
	f.mu.Unlock()
	// A replacement daemon can complete its local handshake while the same
	// retained WAN connection is dead. Both old leases must be released.
	second := f.lease(t)
	secondClosed := make(chan error, 1)
	go func() { secondClosed <- second.Wait() }()
	blocked := make(chan error, 1)
	go func() { blocked <- runCommand(client, "must not be replayed") }()
	select {
	case err := <-blocked:
		if err == nil {
			t.Fatal("command succeeded through a blackholed connection")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("dead retained transport never released the blocked lease")
	}
	select {
	case <-secondClosed:
	case <-time.After(time.Second):
		t.Fatal("replacement daemon remained attached to the dead upstream")
	}
	replacement := f.lease(t)
	if err := runCommand(replacement, "after outage"); err != nil {
		t.Fatal(err)
	}
	if f.dials.Load() != 2 || f.commands.Load() != 2 {
		t.Fatal("did not replace upstream exactly once, or replayed an uncertain command", f.dials.Load(), f.commands.Load())
	}
}

func TestCarrierHealthPreservesOfflineTransportUntilRoutingReturns(t *testing.T) {
	var online atomic.Bool
	online.Store(true)
	network := connectivity.NewMonitor(online.Load, time.Second)
	policy := healthPolicy{interval: 10 * time.Millisecond, timeout: 60 * time.Millisecond, network: network}
	f := newFixture(t, time.Minute, policy)
	client := f.lease(t)
	if err := runCommand(client, "online"); err != nil {
		t.Fatal(err)
	}
	online.Store(false)
	network.Refresh()
	f.mu.Lock()
	f.streams[0].drop.Store(true)
	f.mu.Unlock()
	closed := make(chan error, 1)
	go func() { closed <- client.Wait() }()
	select {
	case <-closed:
		t.Fatal("known offline interval retired a resumable transport")
	case <-time.After(200 * time.Millisecond):
	}
	online.Store(true)
	network.Refresh()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("dead transport was retained after routing returned")
	}
	if err := runCommand(f.lease(t), "restored"); err != nil {
		t.Fatal(err)
	}
}

func TestHealthyGatewayCannotMaskBlockedRuntimeChannels(t *testing.T) {
	f := newFixture(t, time.Minute, healthPolicy{interval: 10 * time.Millisecond, timeout: 100 * time.Millisecond})
	client := f.lease(t)
	if err := runCommand(client, "before blocked runtime"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for f.probes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.probes.Load() == 0 {
		t.Fatal("runtime echo service did not start")
	}
	f.blockRequests.Store(true)
	if ok, _, err := client.SendRequest("probe", true, nil); err != nil || !ok {
		t.Fatal("test gateway is not alive", err)
	}
	blocked := make(chan error, 1)
	go func() { blocked <- runCommand(client, "must not be replayed") }()
	select {
	case err := <-blocked:
		if err == nil {
			t.Fatal("blocked runtime accepted a command")
		}
	case <-time.After(time.Second):
		t.Fatal("global keepalives masked a blocked runtime channel")
	}
	f.blockRequests.Store(false)
	if err := runCommand(f.lease(t), "replacement runtime connection"); err != nil {
		t.Fatal(err)
	}
	if f.dials.Load() != 2 || f.commands.Load() != 2 {
		t.Fatal("replacement missing or uncertain command replayed")
	}
}

func TestSpeculativeLeaseDoesNotStartRuntimeHealthProcess(t *testing.T) {
	f := newFixture(t, time.Minute, healthPolicy{interval: 10 * time.Millisecond, timeout: 100 * time.Millisecond})
	f.lease(t)
	time.Sleep(100 * time.Millisecond)
	if f.channels.Load() != 0 || f.commands.Load() != 0 || f.probes.Load() != 0 {
		t.Fatal("speculative authentication executed a runtime operation before authorization")
	}
}

func TestRuntimeOutputKeepsBusyConnectionAliveWhileEchoIsDelayed(t *testing.T) {
	f := newFixture(t, time.Minute, healthPolicy{interval: 10 * time.Millisecond, timeout: 100 * time.Millisecond})
	release := sync.OnceFunc(func() { close(f.healthGate) })
	defer release()
	client := f.lease(t)
	if err := runCommand(client, "initial"); err != nil {
		t.Fatal(err)
	}
	f.holdHealth.Store(true)
	for i := 0; i < 12; i++ {
		if err := runCommand(client, "runtime is progressing"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	release()
	if err := runCommand(client, "after delayed echo"); err != nil {
		t.Fatal(err)
	}
	if f.dials.Load() != 1 {
		t.Fatal("busy healthy connection was replaced")
	}
}

func TestHealthyCarrierKeepsPeersAcrossHealthChecks(t *testing.T) {
	f := newFixture(t, time.Minute, healthPolicy{interval: 10 * time.Millisecond, timeout: 100 * time.Millisecond})
	first, second := f.lease(t), f.lease(t)
	if err := runCommand(first, "first"); err != nil {
		t.Fatal(err)
	}
	first.Close()
	deadline := time.Now().Add(time.Second)
	for f.probes.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.probes.Load() < 3 {
		t.Fatal("health checks were not exercised")
	}
	if err := runCommand(second, "peer survives"); err != nil {
		t.Fatal(err)
	}
	if f.dials.Load() != 1 {
		t.Fatal("healthy transport replaced", f.dials.Load())
	}
}

func TestLocalIdentityRejected(t *testing.T) {
	f := newFixture(t, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if client, err := dialLocal(ctx, f.socket, testSigner(t)); err == nil {
		client.Close()
		t.Fatal("wrong local identity accepted")
	}
	if f.dials.Load() != 0 {
		t.Fatal("unauthenticated local client dialed WAN")
	}
}

func TestRevocationClosesExistingLeases(t *testing.T) {
	f := newFixture(t, time.Minute)
	client := f.lease(t)
	if err := runCommand(client, "before revoke"); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte{stopRequest})
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revocation kept upstream lease")
	}
}

func TestScopesCannotShareCarrier(t *testing.T) {
	base := Config{Address: "ssh.example:22", User: "runtime", HostKey: []byte("host"), ClientKey: []byte("client"), KeyPath: "/private/key", Scope: "account"}
	changes := []func(*Config){func(c *Config) { c.Address = "other:22" }, func(c *Config) { c.User = "other" }, func(c *Config) { c.HostKey = []byte("rotated") }, func(c *Config) { c.ClientKey = []byte("rotated") }, func(c *Config) { c.Scope = "other" }, func(c *Config) { c.KeyPath = "/other/key" }}
	for _, change := range changes {
		next := base
		change(&next)
		if next.key() == base.key() {
			t.Fatal("identity boundary did not change carrier")
		}
	}
}

func TestUnusedCarrierExpires(t *testing.T) {
	f := newFixture(t, 50*time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(f.socket); os.IsNotExist(err) {
			if f.dials.Load() != 0 {
				t.Fatal("unused worker connected")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("idle carrier did not release its socket")
}
