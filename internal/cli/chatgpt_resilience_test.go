package cli

import (
	"bufio"
	"context"
	"errors"
	"fennec/internal/terminalstream"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAppServer answers the rewritten upgrade like codex app-server and then
// echoes; requests records each upgrade it received with the bytes behind it.
type fakeAppServer struct {
	failFirst atomic.Int32 // dials to fail before answering
	mu        sync.Mutex
	requests  []string
}

func (server *fakeAppServer) dial(context.Context, string) (net.Conn, error) {
	if server.failFirst.Add(-1) >= 0 {
		return nil, errors.New("app-server socket not ready")
	}
	local, remote := net.Pipe()
	go func() {
		defer remote.Close()
		reader := bufio.NewReader(remote)
		var head strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			head.WriteString(line)
			if line == "\r\n" {
				break
			}
		}
		server.mu.Lock()
		server.requests = append(server.requests, head.String())
		server.mu.Unlock()
		io.WriteString(remote, "HTTP/1.1 101 Switching Protocols\r\nupgrade: websocket\r\nsec-websocket-accept: x\r\n\r\n")
		io.Copy(remote, reader)
	}()
	return local, nil
}

func startFakeCloud(t *testing.T, ctx context.Context, server *fakeAppServer) *terminalChannel {
	t.Helper()
	serverInput, clientInput := io.Pipe()
	serverOutput, clientOutput := io.Pipe()
	t.Cleanup(func() { clientInput.Close(); serverOutput.Close() })
	go terminalstream.ServeWithSockets(ctx, serverInput, clientOutput, nil, nil, nil, nil, server.dial)
	transport, err := startTestTransport(ctx, clientInput, serverOutput)
	if err != nil {
		t.Fatal(err)
	}
	return &terminalChannel{ctx: ctx, transport: transport}
}

// bridgeWithRememberedHandshake is a bridge that has served ChatGPT before.
func bridgeWithRememberedHandshake(t *testing.T, ctx context.Context, services *daemonServices) string {
	t.Helper()
	bridge := newChatGPTBridge(t.TempDir(), services)
	bridge.pref.Token = bridgeTestToken
	bridge.pref.EarlyInput = true
	bridge.upgrade = []byte("HTTP/1.1 101 Switching Protocols\r\nupgrade: websocket\r\n")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go bridge.accept(ctx, listener)
	return listener.Addr().String()
}

// connectChatGPT opens a WebSocket upgrade with a request already behind it,
// as ChatGPT sends initialize right after the 101, and returns the reader once
// the handshake answer arrived.
func connectChatGPT(t *testing.T, address string) (net.Conn, *bufio.Reader, time.Duration) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	started := time.Now()
	io.WriteString(conn, "GET /fennec/"+bridgeTestToken+" HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\ninitialize")
	reader := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	status, err := reader.ReadString('\n')
	if err != nil || status != "HTTP/1.1 101 Switching Protocols\r\n" {
		t.Fatalf("handshake: %q %v", status, err)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	return conn, reader, time.Since(started)
}

func expectEcho(t *testing.T, reader *bufio.Reader, want string) {
	t.Helper()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != want {
		t.Fatalf("echo %q, %v; want %q", got, err, want)
	}
}

// With the cloud link down (network change, weak network) ChatGPT's
// connection must not fail: a failed attempt fails every queued request and
// leaves ChatGPT showing "Permission mode is unavailable" until it restarts.
func TestChatGPTBridgeHoldsRequestsWhileTheCloudIsAway(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	services := &daemonServices{} // no resident channel: the cloud is unreachable
	address := bridgeWithRememberedHandshake(t, ctx, services)

	_, reader, handshake := connectChatGPT(t, address)
	if handshake > time.Second {
		t.Fatalf("handshake took %v while the cloud was away; ChatGPT gives up after about 15 s", handshake)
	}
	// The cloud comes back after ChatGPT would have given up on a handshake.
	server := &fakeAppServer{}
	time.Sleep(2 * time.Second)
	channel := startFakeCloud(t, ctx, server)
	services.mu.Lock()
	services.channel = channel
	services.mu.Unlock()
	expectEcho(t, reader, "initialize")
}

// A stream that fails before the cloud confirms it (a half-open channel after
// a network change, an app-server still starting) is replaced and the bytes
// ChatGPT already sent are replayed, so ChatGPT never sees the failure.
func TestChatGPTBridgeReplaysAnUnconfirmedStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	server := &fakeAppServer{}
	server.failFirst.Store(1)
	services := &daemonServices{channel: startFakeCloud(t, ctx, server)}
	address := bridgeWithRememberedHandshake(t, ctx, services)

	conn, reader, _ := connectChatGPT(t, address)
	expectEcho(t, reader, "initialize")
	io.WriteString(conn, " then more")
	expectEcho(t, reader, " then more")
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.requests) != 1 || !strings.HasPrefix(server.requests[0], "GET /rpc HTTP/1.1\r\n") {
		t.Fatalf("app-server upgrades: %q", server.requests)
	}
}
