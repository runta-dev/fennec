package localssh

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

// Long-lived connections (desktop MCP servers) must not starve new commands:
// claimed device sockets are replaced immediately, beyond the 8 parked ones.
func TestRelayKeepsIdleBridgesWhileConnectionsStayOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Echo server stands in for the local connector.
	echo, _ := net.Listen("tcp", "127.0.0.1:0")
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
	// Bridge listener stands in for relay.py's bridge port.
	bridges, _ := net.Listen("tcp", "127.0.0.1:0")
	defer bridges.Close()
	parked := make(chan net.Conn, 128)
	go func() {
		for {
			c, err := bridges.Accept()
			if err != nil {
				return
			}
			parked <- c
		}
	}()
	// In-process SSH server provides the direct-tcpip channels.
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	server := &gliderssh.Server{
		Handler:                     func(gliderssh.Session) {},
		PasswordHandler:             func(gliderssh.Context, string) bool { return true },
		LocalPortForwardingCallback: func(gliderssh.Context, string, uint32) bool { return true },
		ChannelHandlers:             map[string]gliderssh.ChannelHandler{"direct-tcpip": gliderssh.DirectTCPIPHandler, "session": gliderssh.DefaultSessionHandler},
	}
	server.AddHostKey(signer)
	sshListener, _ := net.Listen("tcp", "127.0.0.1:0")
	go server.Serve(sshListener)
	defer server.Close()
	client, err := ssh.Dial("tcp", sshListener.Addr().String(), &ssh.ClientConfig{User: "t", Auth: []ssh.AuthMethod{ssh.Password("x")}, HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	port := bridges.Addr().(*net.TCPAddr).Port
	go serveRelay(ctx, client, port, echo.Addr().String())

	claim := func() net.Conn {
		select {
		case c := <-parked:
			if _, err := c.Write([]byte{'F'}); err != nil {
				t.Fatal(err)
			}
			return c
		case <-time.After(time.Second):
			return nil
		}
	}
	var open []net.Conn
	for i := 0; i < 20; i++ {
		c := claim()
		if c == nil {
			t.Fatalf("no idle bridge after %d long-lived connections", i)
		}
		open = append(open, c)
		msg := "ping " + strconv.Itoa(i) + "\n"
		c.Write([]byte(msg))
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := bufio.NewReader(c).ReadString('\n')
		if err != nil || line != msg {
			t.Fatalf("connection %d did not forward: %q %v", i, line, err)
		}
	}
	if claim() == nil {
		t.Fatal("a new command must still find an idle bridge while 20 connections stay open")
	}
	for _, c := range open {
		c.Close()
	}
}
