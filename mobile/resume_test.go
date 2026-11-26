package fennecmobile

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// pipeClient is an SSH connection over loopback TCP. A server that
// doesn't answer stands for a link that died while the app was suspended:
// requests go out and nothing comes back.
func pipeClient(t *testing.T, answers bool) *ssh.Client {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	server := &ssh.ServerConfig{NoClientAuth: true}
	server.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		b, err := listener.Accept()
		if err != nil {
			return
		}
		conn, channels, requests, err := ssh.NewServerConn(b, server)
		if err != nil {
			return
		}
		defer conn.Close()
		go func() {
			for channel := range channels {
				channel.Reject(ssh.Prohibited, "test")
			}
		}()
		for request := range requests {
			if answers && request.WantReply {
				request.Reply(true, nil)
			}
		}
	}()
	a, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn, channels, requests, err := ssh.NewClientConn(a, "pipe", &ssh.ClientConfig{HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(conn, channels, requests)
	t.Cleanup(func() { client.Close() })
	return client
}

func suspended(t *testing.T, old *ssh.Client, dial func(context.Context) (*ssh.Client, string, string, error)) *Sessions {
	s := &Sessions{directory: t.TempDir(), client: old, runtimeID: "r1", token: "t"}
	s.dialHook = dial
	s.Suspend()
	return s
}

// A connection that still works is kept: one keepalive, no new connection.
func TestSettleKeepsALiveConnection(t *testing.T) {
	old := pipeClient(t, true)
	var dials atomic.Int32
	s := suspended(t, old, func(context.Context) (*ssh.Client, string, string, error) {
		dials.Add(1)
		return pipeClient(t, true), "r1", "t", nil
	})
	if err := s.settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.client != old || dials.Load() != 0 {
		t.Fatalf("kept old=%v dials=%d", s.client == old, dials.Load())
	}
	if s.suspect || s.resuming != nil {
		t.Fatal("still checking after settle")
	}
}

// FEN-68: a link that died silently doesn't cost a keepalive timeout
// first; a new connection is dialed after resumeGrace, and every caller
// waiting meanwhile gets it.
func TestSettleReplacesADeadConnectionQuickly(t *testing.T) {
	old := pipeClient(t, false)
	fresh := pipeClient(t, true)
	var dials atomic.Int32
	s := suspended(t, old, func(context.Context) (*ssh.Client, string, string, error) {
		dials.Add(1)
		time.Sleep(50 * time.Millisecond)
		return fresh, "r1", "t", nil
	})
	started := time.Now()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.settle(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if took := time.Since(started); took > resumeGrace+time.Second {
		t.Fatalf("settled after %v", took)
	}
	s.mu.Lock()
	current := s.client
	s.mu.Unlock()
	if current != fresh || dials.Load() != 1 {
		t.Fatalf("fresh=%v dials=%d", current == fresh, dials.Load())
	}
	if _, _, err := old.SendRequest("keepalive@openssh.com", true, nil); err == nil {
		t.Fatal("the dead connection is still open")
	}
}

// A connection that reports itself closed is dropped at once, without
// waiting for the grace period; connect then dials as usual.
func TestSettleDropsAClosedConnection(t *testing.T) {
	old := pipeClient(t, true)
	old.Close()
	s := suspended(t, old, func(context.Context) (*ssh.Client, string, string, error) {
		t.Error("dialed during the check")
		return nil, "", "", context.Canceled
	})
	if err := s.settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.client != nil {
		t.Fatal("closed connection kept")
	}
}

// Without Suspend nothing is checked: calls in the foreground cost nothing.
func TestSettleWithoutSuspendDoesNothing(t *testing.T) {
	old := pipeClient(t, false)
	s := &Sessions{client: old}
	done := make(chan error, 1)
	go func() { done <- s.settle(context.Background()) }()
	select {
	case err := <-done:
		if err != nil || s.client != old {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("settle waited without Suspend")
	}
}
