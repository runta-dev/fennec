package localssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	glider "github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func newSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// latencyConn delays bytes in both directions by delay, like a long link.
type latencyConn struct {
	net.Conn
	delay  time.Duration
	writes chan timedChunk
	reads  chan timedChunk
	rest   []byte
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

type timedChunk struct {
	at   time.Time
	data []byte
}

func withLatency(conn net.Conn, delay time.Duration) net.Conn {
	c := &latencyConn{Conn: conn, delay: delay, writes: make(chan timedChunk, 1024), reads: make(chan timedChunk, 1024)}
	go func() {
		for chunk := range c.writes {
			time.Sleep(time.Until(chunk.at))
			if _, err := conn.Write(chunk.data); err != nil {
				return
			}
		}
	}()
	go func() {
		defer close(c.reads)
		buffer := make([]byte, 32<<10)
		for {
			n, err := conn.Read(buffer)
			if n > 0 {
				c.reads <- timedChunk{time.Now().Add(delay), append([]byte(nil), buffer[:n]...)}
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *latencyConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	c.writes <- timedChunk{time.Now().Add(c.delay), append([]byte(nil), data...)}
	return len(data), nil
}

func (c *latencyConn) Read(data []byte) (int, error) {
	if len(c.rest) == 0 {
		chunk, ok := <-c.reads
		if !ok {
			return 0, io.EOF
		}
		time.Sleep(time.Until(chunk.at))
		c.rest = chunk.data
	}
	n := copy(data, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}

func (c *latencyConn) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		close(c.writes)
		c.mu.Unlock()
	})
	return c.Conn.Close()
}

type relayHarness struct {
	mac      string // the Mac's listening address
	macKey   gossh.Signer
	identity gossh.Signer
	relayKey gossh.Signer
	delay    time.Duration
	opened   chan struct{} // one value per session channel the Mac accepted
}

// newMacServer runs a stand-in for the Mac's SSH service: it echoes the
// command, copies stdin for "cat", writes stderr and exits 3 for "fail".
func newMacServer(t *testing.T, delay time.Duration) *relayHarness {
	t.Helper()
	h := &relayHarness{macKey: newSigner(t), identity: newSigner(t), relayKey: newSigner(t), delay: delay, opened: make(chan struct{}, 64)}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.mac = listener.Addr().String()
	allowed := h.identity.PublicKey().Marshal()
	server := &glider.Server{
		PublicKeyHandler: func(_ glider.Context, key glider.PublicKey) bool { return bytes.Equal(key.Marshal(), allowed) },
		Handler: func(session glider.Session) {
			switch command := session.RawCommand(); command {
			case "cat":
				io.Copy(session, session)
				session.Exit(0)
			case "fail":
				io.WriteString(session.Stderr(), "no such file\n")
				session.Exit(3)
			default:
				io.WriteString(session, "ran "+command+"\n")
				session.Exit(0)
			}
		},
		ChannelHandlers: map[string]glider.ChannelHandler{"session": func(srv *glider.Server, conn *gossh.ServerConn, channel gossh.NewChannel, ctx glider.Context) {
			h.opened <- struct{}{}
			glider.DefaultSessionHandler(srv, conn, channel, ctx)
		}},
	}
	server.AddHostKey(h.macKey)
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return h
}

func (h *relayHarness) options() MacRelayOptions {
	return MacRelayOptions{HostKey: h.relayKey, Identity: h.identity, MacHostKey: h.macKey.PublicKey(), Dial: func(ctx context.Context) (net.Conn, error) {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", h.mac)
		if err != nil {
			return nil, err
		}
		return withLatency(conn, h.delay), nil
	}}
}

// client connects an ssh client to a relay the way ProxyCommand does.
func (h *relayHarness) client(t *testing.T, options MacRelayOptions) *gossh.Client {
	t.Helper()
	local, relay := socketPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunMacRelay(ctx, relay, options); close(done) }()
	connection, channels, requests, err := gossh.NewClientConn(local, "relay", &gossh.ClientConfig{User: "fennec", Auth: []gossh.AuthMethod{gossh.PublicKeys(h.identity)}, HostKeyCallback: gossh.FixedHostKey(h.relayKey.PublicKey())})
	if err != nil {
		t.Fatal(err)
	}
	client := gossh.NewClient(connection, channels, requests)
	t.Cleanup(func() { client.Close(); cancel(); <-done })
	return client
}

func run(t *testing.T, client *gossh.Client, command, input string) (string, string, error) {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
	if input != "" {
		session.Stdin = strings.NewReader(input)
	}
	err = session.Run(command)
	return stdout.String(), stderr.String(), err
}

func TestMacRelayRunsCommandsWithOutputAndStatus(t *testing.T) {
	h := newMacServer(t, 0)
	client := h.client(t, h.options())
	if out, _, err := run(t, client, "uname -a", ""); err != nil || out != "ran uname -a\n" {
		t.Fatal(out, err)
	}
	payload := strings.Repeat("0123456789", 300_000) // beyond one SSH window
	if out, _, err := run(t, client, "cat", payload); err != nil || out != payload {
		t.Fatal(len(out), err)
	}
	_, stderr, err := run(t, client, "fail", "")
	var exit *gossh.ExitError
	if !errors.As(err, &exit) || exit.ExitStatus() != 3 || stderr != "no such file\n" {
		t.Fatal(stderr, err)
	}
	// Concurrent sessions beyond the pool still work.
	var group sync.WaitGroup
	for i := 0; i < 6; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			if out, _, err := run(t, client, fmt.Sprint("job ", i), ""); err != nil || out != fmt.Sprintf("ran job %d\n", i) {
				t.Error(out, err)
			}
		}(i)
	}
	group.Wait()
}

// The point of the relay: with channels already open on the Mac, a command
// costs one round trip, where plain ssh on a live connection costs two.
func TestMacRelayCommandTakesOneRoundTrip(t *testing.T) {
	const oneWay = 60 * time.Millisecond
	h := newMacServer(t, oneWay)
	client := h.client(t, h.options())
	run(t, client, "warm up", "")
	// Wait for the pool to be refilled on the Mac.
	deadline := time.After(5 * time.Second)
	for count := 0; count < 1+relayPoolSize; count++ {
		select {
		case <-h.opened:
		case <-deadline:
			t.Fatal("pool not refilled")
		}
	}
	time.Sleep(4 * oneWay)
	start := time.Now()
	if out, _, err := run(t, client, "true", ""); err != nil || out != "ran true\n" {
		t.Fatal(out, err)
	}
	if elapsed := time.Since(start); elapsed > 3*oneWay {
		t.Fatalf("command took %v; one round trip is %v", elapsed, 2*oneWay)
	}
}

func TestMacRelayRejectsUnknownClientsAndReportsAnOfflineMac(t *testing.T) {
	h := newMacServer(t, 0)
	local, relay := socketPair(t)
	go RunMacRelay(context.Background(), relay, h.options())
	stranger := newSigner(t)
	if _, _, _, err := gossh.NewClientConn(local, "relay", &gossh.ClientConfig{User: "fennec", Auth: []gossh.AuthMethod{gossh.PublicKeys(stranger)}, HostKeyCallback: gossh.FixedHostKey(h.relayKey.PublicKey())}); err == nil {
		t.Fatal("relay accepted an unknown key")
	}
	offline := h.options()
	offline.Dial = func(context.Context) (net.Conn, error) { return nil, errors.New("connection refused") }
	client := h.client(t, offline)
	_, stderr, err := run(t, client, "true", "")
	var exit *gossh.ExitError
	if !errors.As(err, &exit) || exit.ExitStatus() != 255 || !strings.Contains(stderr, "not reachable") {
		t.Fatal(stderr, err)
	}
	// A Mac presenting another host key is refused.
	impostor := h.options()
	impostor.MacHostKey = newSigner(t).PublicKey()
	client = h.client(t, impostor)
	if _, _, err := run(t, client, "true", ""); err == nil {
		t.Fatal("relay accepted an unexpected Mac host key")
	}
}

func TestRelayHostKeyIsCreatedOnceAndTrusted(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureRelayHostKey(dir); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, relayHostKeyFile))
	if err := EnsureRelayHostKey(dir); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, relayHostKeyFile))
	known, _ := os.ReadFile(filepath.Join(dir, relayKnownHostsFile))
	signer, err := gossh.ParsePrivateKey(second)
	if err != nil || !bytes.Equal(first, second) || string(known) != relayHostKeyAlias+" "+string(gossh.MarshalAuthorizedKey(signer.PublicKey())) {
		t.Fatal(string(known), err)
	}
}

// socketPair returns two ends of a buffered connection; like ProxyCommand
// stdio, and unlike net.Pipe, both ends can send their SSH banners at once.
func socketPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	local, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return local, <-accepted
}
