package localssh

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
)

// One parked bridge that the cloud closes (its idle set is bounded, or it
// evicted a dead peer) is parked again; the relay keeps carrying connections.
// It stopped the whole relay before, dropping every forwarded connection.
// Once the cloud relay is gone for good, the relay still stops, so Link
// reconciles it.
func TestRelaySurvivesOneClosedBridgeAndStopsWhenTheCloudIsGone(t *testing.T) {
	old := bridgeRetryDelay
	bridgeRetryDelay = 10 * time.Millisecond
	defer func() { bridgeRetryDelay = old }()
	echo, err := net.Listen("tcp", "127.0.0.1:0") // the Mac's SSH service
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	bridges, err := net.Listen("tcp", "127.0.0.1:0") // the cloud relay
	if err != nil {
		t.Fatal(err)
	}
	client := linkLeaseClient(t, func(server *gliderssh.Server) {
		server.LocalPortForwardingCallback = func(gliderssh.Context, string, uint32) bool { return true }
		server.ChannelHandlers = map[string]gliderssh.ChannelHandler{"direct-tcpip": gliderssh.DirectTCPIPHandler, "session": gliderssh.DefaultSessionHandler}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serveRelay(ctx, client, bridges.Addr().(*net.TCPAddr).Port, echo.Addr().String(), false)
	}()
	accept := func() net.Conn {
		t.Helper()
		bridges.(*net.TCPListener).SetDeadline(time.Now().Add(3 * time.Second))
		c, err := bridges.Accept()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	accept().Close() // the cloud drops one idle bridge
	for i := 0; i < parkedBridges; i++ {
		c := accept()
		defer c.Close()
		if i < 2 {
			c.Write([]byte{'F'})
			c.Write([]byte("ping"))
			c.SetReadDeadline(time.Now().Add(3 * time.Second))
			got := make([]byte, 4)
			if _, err := io.ReadFull(c, got); err != nil || string(got) != "ping" {
				t.Fatalf("connection %d through the relay: %q %v", i, got, err)
			}
		}
	}
	select {
	case err := <-done:
		t.Fatalf("relay stopped after one closed bridge: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	bridges.Close() // the cloud relay is gone
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("relay stopped without an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay kept running after the cloud relay was gone")
	}
	assertLinkPeerAlive(t, client)
}
