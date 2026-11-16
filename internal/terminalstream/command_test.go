package terminalstream

import (
	"bytes"
	"context"
	"fennec/internal/commandstream"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestCommandTransferUsesResidentWhilePingRemainsResponsive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := socketTransport(t, func(ctx context.Context, target string) (net.Conn, error) {
		if target != SocketCommand {
			t.Errorf("unexpected target %s", target)
		}
		return commandstream.Dial(ctx), nil
	})
	// A zero SSH client cannot open a channel: all command bytes must use the
	// already running resident, including stdin EOF, stderr and exit status.
	client := new(ssh.Client)
	unregister := commandstream.Register(client, func(ctx context.Context) (net.Conn, error) { return transport.DialSocket(ctx, SocketCommand, "") })
	defer unregister()
	s, err := commandstream.NewSession(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	payload := bytes.Repeat([]byte{0, 255, 4, 7}, 65536)
	s.Stdin = bytes.NewReader(payload)
	done := make(chan error, 1)
	go func() {
		got, err := s.Output("cat")
		if err == nil && !bytes.Equal(got, payload) {
			err = io.ErrUnexpectedEOF
		}
		done <- err
	}()
	count := 0
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if count < 5 {
				t.Fatal("insufficient loaded pings", count)
			}
			return
		default:
			rtt, err := transport.Ping(ctx)
			if err != nil || rtt > 100*time.Millisecond {
				t.Fatalf("command delayed ping: %s %v", rtt, err)
			}
			count++
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestCommandCapacityReservesInteractiveStreams(t *testing.T) {
	transport := socketTransport(t, func(ctx context.Context, _ string) (net.Conn, error) {
		local, remote := net.Pipe()
		context.AfterFunc(ctx, func() { remote.Close() })
		return local, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var commands []net.Conn
	defer func() {
		for _, c := range commands {
			c.Close()
		}
	}()
	for range 16 {
		c, err := transport.DialSocket(ctx, SocketCommand, "")
		if err != nil {
			t.Fatal(err)
		}
		commands = append(commands, c)
	}
	if c, err := transport.DialSocket(ctx, SocketCommand, ""); err == nil {
		c.Close()
		t.Fatal("unbounded command admission")
	}
	c, err := transport.DialSocket(ctx, SocketCodexAppServer, "")
	if err != nil {
		t.Fatal("commands consumed normal admission", err)
	}
	defer c.Close()
	if _, err := transport.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}
