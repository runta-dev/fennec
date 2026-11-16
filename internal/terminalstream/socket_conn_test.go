package terminalstream

import (
	"bytes"
	"context"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"io"
	"net"
	"testing"
	"time"
)

func socketTransport(t *testing.T, dial SocketDialer) *Transport {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	input, send := io.Pipe()
	receive, output := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- ServeWithSockets(ctx, input, output, nil, nil, nil, func(context.Context, cloudruntime.LocalOperation) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		}, dial)
	}()
	transport := StartPipes(send, receive)
	t.Cleanup(func() {
		transport.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("socket service did not stop")
		}
	})
	return transport
}

func TestSocketConnFullDuplexWhileReaderStalls(t *testing.T) {
	payload := bytes.Repeat([]byte("full-duplex-data!"), 8192)
	targetReady := make(chan net.Conn, 1)
	transport := socketTransport(t, func(ctx context.Context, target string) (net.Conn, error) {
		if target != SocketDeviceRelay || SocketDevice(ctx) != "1234abcd" {
			t.Error("relay identity lost")
		}
		local, remote := net.Pipe()
		targetReady <- remote
		return local, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := transport.DialSocket(ctx, SocketDeviceRelay, "1234abcd")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	remote := <-targetReady
	defer remote.Close()
	downloads := make(chan error, 1)
	go func() { _, err := remote.Write(payload); downloads <- err }()
	uploads := make(chan error, 1)
	go func() {
		got := make([]byte, len(payload))
		_, err := io.ReadFull(remote, got)
		if err == nil && !bytes.Equal(got, payload) {
			err = io.ErrUnexpectedEOF
		}
		uploads <- err
	}()
	// The peer writes more than the output window, and this client deliberately
	// reads nothing yet. Upload credits must nevertheless keep flowing.
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	written := make(chan error, 1)
	go func() { _, err := conn.Write(payload); written <- err }()
	for range 10 {
		if rtt, err := transport.Ping(ctx); err != nil || rtt > 100*time.Millisecond {
			t.Fatalf("stalled reader delayed ping: %s %v", rtt, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("stalled reader also stopped uploads")
	}
	if err := <-uploads; err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatal("download changed", err)
	}
	if err := <-downloads; err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSocketConnDrainsDataBeforeEOFAndHonorsDeadline(t *testing.T) {
	target := make(chan net.Conn, 1)
	transport := socketTransport(t, func(context.Context, string) (net.Conn, error) {
		local, remote := net.Pipe()
		target <- remote
		return local, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := transport.DialSocket(ctx, SocketCodexAppServer, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer := <-target
	conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	var one [1]byte
	if _, err := conn.Read(one[:]); err == nil {
		t.Fatal("read deadline ignored")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	payload := bytes.Repeat([]byte("tail"), 800)
	go func() { peer.Write(payload); peer.Close() }()
	got, err := io.ReadAll(conn)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("lost tail at EOF", len(got), err)
	}
	if _, err := transport.Ping(ctx); err != nil {
		t.Fatal("closing socket damaged shared transport", err)
	}
}

func TestDeviceRelayCapacityLeavesNormalStreamsAvailable(t *testing.T) {
	transport := socketTransport(t, func(ctx context.Context, _ string) (net.Conn, error) {
		local, remote := net.Pipe()
		context.AfterFunc(ctx, func() { remote.Close() })
		return local, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var conns []net.Conn
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()
	for range 80 {
		conn, err := transport.DialSocket(ctx, SocketDeviceRelay, "1234abcd")
		if err != nil {
			t.Fatal("relay capacity exhausted early", len(conns), err)
		}
		conns = append(conns, conn)
	}
	if conn, err := transport.DialSocket(ctx, SocketDeviceRelay, "1234abcd"); err == nil {
		conn.Close()
		t.Fatal("unbounded relay connections")
	}
	normal, err := transport.DialSocket(ctx, SocketCodexAppServer, "")
	if err != nil {
		t.Fatal("reverse connections exhausted normal streams", err)
	}
	defer normal.Close()
	if _, err := transport.Operate(ctx, cloudruntime.LocalOperation{Kind: "list"}); err != nil {
		t.Fatal("reverse connections exhausted control capacity", err)
	}
}

func TestDeviceRelayRejectsUnscopedTargets(t *testing.T) {
	for _, device := range []string{"", "../device", "127.0.0.1:22", "a", "ABCDEF12"} {
		if (Frame{Type: "socket", ID: "12345678901234567890123456789012", Target: SocketDeviceRelay, Message: device}).validateOpen() == nil {
			t.Fatalf("invalid device accepted: %q", device)
		}
	}
}
