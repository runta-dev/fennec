package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/terminalstream"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func connectorTransport(t *testing.T) *terminalstream.Transport {
	t.Helper()
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		encoder, decoder := json.NewEncoder(server), json.NewDecoder(server)
		encoder.Encode(terminalstream.Frame{Type: "ready", Version: terminalstream.ProtocolVersion})
		for {
			var frame terminalstream.Frame
			if decoder.Decode(&frame) != nil {
				return
			}
			if frame.Type == "ping" {
				encoder.Encode(terminalstream.Frame{Type: "pong"})
			}
		}
	}()
	transport := terminalstream.StartPipes(client, client)
	t.Cleanup(transport.Abort)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	return transport
}

func TestConnectorBorrowsOnlyVerifiedAccountAndRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &ssh.Client{}
	s := &daemonServices{ctx: ctx, epoch: 7, config: brokerConfiguration{RuntimeID: "owned", PreparedRuntimeID: "owned", PreparedSSH: client}}
	got, err := s.linkSSH(ctx, 7, "owned")
	if err != nil || got != client {
		t.Fatal("verified startup connection not reused", err)
	}
	for _, tc := range []struct {
		epoch   uint64
		runtime string
	}{{6, "owned"}, {7, "other"}} {
		if _, err := s.linkSSH(ctx, tc.epoch, tc.runtime); err == nil {
			t.Fatal("stale scope accepted")
		}
	}
	cancel()
	if _, err := s.linkSSH(context.Background(), 7, "owned"); err == nil {
		t.Fatal("signed-out lifecycle accepted")
	}
}

func TestConnectorBorrowersWaitForOneResidentRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	transport := connectorTransport(t)
	pending := make(chan struct{})
	channel := &terminalChannel{ctx: ctx, pending: pending}
	type result struct {
		client *ssh.Client
		err    error
	}
	done := make(chan result, 8)
	for range 8 {
		go func() { client, err := channel.borrowSSH(ctx); done <- result{client, err} }()
	}
	select {
	case <-done:
		t.Fatal("borrowed before recovery completed")
	case <-time.After(10 * time.Millisecond):
	}
	verified := &ssh.Client{}
	channel.mu.Lock()
	channel.preparedSSH = verified
	channel.transport = transport
	channel.pending = nil
	close(pending)
	channel.mu.Unlock()
	for range 8 {
		select {
		case got := <-done:
			if got.err != nil || got.client != verified {
				t.Fatal("borrower did not share recovery", got.err)
			}
		case <-ctx.Done():
			t.Fatal("borrower stuck")
		}
	}
}

func TestConnectorRechecksScopeAfterWaitingForRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	transport := connectorTransport(t)
	pending := make(chan struct{})
	channel := &terminalChannel{ctx: ctx, pending: pending}
	s := &daemonServices{ctx: ctx, epoch: 7, config: brokerConfiguration{RuntimeID: "owned"}, channel: channel}
	done := make(chan error, 1)
	go func() { _, err := s.linkSSH(ctx, 7, "owned"); done <- err }()
	select {
	case <-done:
		t.Fatal("returned before recovery")
	case <-time.After(10 * time.Millisecond):
	}
	s.mu.Lock()
	s.epoch = 8
	s.mu.Unlock()
	channel.mu.Lock()
	channel.preparedSSH = &ssh.Client{}
	channel.transport = transport
	channel.pending = nil
	close(pending)
	channel.mu.Unlock()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("late connection crossed account boundary")
		}
	case <-ctx.Done():
		t.Fatal("borrower stuck")
	}
}

func TestConnectorReverseBytesUseResidentWithoutNewSSHChannels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	seen := make(chan string, 1)
	channel := startFakeCloudWith(t, ctx, func(ctx context.Context, target string) (net.Conn, error) {
		seen <- target + "/" + terminalstream.SocketDevice(ctx)
		local, remote := net.Pipe()
		go func() { defer remote.Close(); io.Copy(remote, remote) }()
		return local, nil
	})
	services := &daemonServices{ctx: ctx, epoch: 7, config: brokerConfiguration{RuntimeID: "owned"}, channel: channel}
	// No prepared SSH client exists: opening a direct-tcpip channel would fail.
	conn, err := services.dialResidentRelay(ctx, 7, "owned", "1234abcd", 12345)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if target := <-seen; target != terminalstream.SocketDeviceRelay+"/1234abcd" {
		t.Fatal(target)
	}
	conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("reverse")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 7)
	if _, err := io.ReadFull(conn, data); err != nil || string(data) != "reverse" {
		t.Fatal(string(data), err)
	}
	conn.Close()
	if _, err := channel.transport.Ping(ctx); err != nil {
		t.Fatal("reverse close damaged resident", err)
	}
}

func TestConnectorReverseDialRechecksAccountAfterOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	opening, release := make(chan struct{}), make(chan struct{})
	channel := startFakeCloudWith(t, ctx, func(context.Context, string) (net.Conn, error) {
		close(opening)
		<-release
		local, remote := net.Pipe()
		context.AfterFunc(ctx, func() { remote.Close() })
		return local, nil
	})
	services := &daemonServices{ctx: ctx, epoch: 7, config: brokerConfiguration{RuntimeID: "owned"}, channel: channel}
	result := make(chan error, 1)
	go func() {
		conn, err := services.dialResidentRelay(ctx, 7, "owned", "1234abcd", 12345)
		if conn != nil {
			conn.Close()
		}
		result <- err
	}()
	select {
	case <-opening:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	services.mu.Lock()
	services.epoch++
	services.mu.Unlock()
	close(release)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("published reverse stream from an obsolete account")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
