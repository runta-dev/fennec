package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/commandstream"
	"fennec/internal/control"
	"fennec/internal/terminalstream"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func managedTransferFixture(t *testing.T, limits ...time.Duration) (*daemonServices, context.Context, context.CancelFunc) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "fen-transfer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	limit := 10 * time.Second
	if len(limits) > 0 {
		limit = limits[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	input, send := io.Pipe()
	receive, output := io.Pipe()
	server := make(chan error, 1)
	go func() {
		defer output.Close()
		server <- terminalstream.ServeWithSockets(ctx, input, output, nil, nil, nil, nil, func(ctx context.Context, _ string) (net.Conn, error) { return commandstream.Dial(ctx), nil })
	}()
	transport := terminalstream.StartPipes(send, receive)
	if err := transport.Ready(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	s := &daemonServices{home: home, ctx: ctx, cancel: cancel, user: "owner", epoch: 7, config: brokerConfiguration{RuntimeID: "owned"}, channel: &terminalChannel{ctx: ctx, transport: transport}}
	ready := make(chan struct{})
	controlDone := make(chan error, 1)
	go func() {
		controlDone <- control.Serve(ctx, filepath.Join(home, ".config/fennec/ipc/control.sock"), map[string]control.Handler{"transfer.open": s.openTransfer}, func() { close(ready) })
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	t.Cleanup(func() { cancel(); transport.Close(); <-server; <-controlDone })
	return s, ctx, cancel
}

func TestManagedTransferUsesScopedResidentAndPreservesExitCode(t *testing.T) {
	s, ctx, _ := managedTransferFixture(t)
	t.Setenv("HOME", s.home)
	if PublicInvocation([]string{"transfer-ssh"}) {
		t.Fatal("private wrapper dispatched as public command")
	}
	payload := bytes.Repeat([]byte{0, 255, 17, 8}, 16384)
	var output, stderr bytes.Buffer
	err := transferSSH([]string{"owner", "owned", "--", "fennec-cloud", "cat; printf diagnostic >&2; exit 37"}, bytes.NewReader(payload), &output, &stderr)
	var status interface{ ExitCode() int }
	if !errors.As(err, &status) || status.ExitCode() != 37 || !bytes.Equal(output.Bytes(), payload) || stderr.String() != "diagnostic" {
		t.Fatalf("lost command streams/status: %d %q %v", output.Len(), stderr.String(), err)
	}
	if _, err := openManagedCommand(ctx, s.home, "other-owner", "owned"); err == nil {
		t.Fatal("cross-account transfer accepted")
	}
	if _, err := openManagedCommand(ctx, s.home, "owner", "other-runtime"); err == nil {
		t.Fatal("cross-runtime transfer accepted")
	}
	if _, err := s.channel.transport.Ping(ctx); err != nil {
		t.Fatal("command exit destroyed resident", err)
	}
}

func TestManagedTransferRechecksScopeAfterRecovery(t *testing.T) {
	s, ctx, _ := managedTransferFixture(t)
	transport := s.channel.transport
	pending := make(chan struct{})
	channel := &terminalChannel{ctx: ctx, pending: pending}
	s.channel = channel
	done := make(chan error, 1)
	go func() {
		conn, err := s.dialTransfer(ctx, 7, transferScope{Owner: "owner", RuntimeID: "owned"})
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("returned before recovery")
	case <-time.After(10 * time.Millisecond):
	}
	s.mu.Lock()
	s.epoch = 8
	s.mu.Unlock()
	channel.mu.Lock()
	channel.transport = transport
	channel.pending = nil
	close(pending)
	channel.mu.Unlock()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stale transfer accepted")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestManagedTransferBoundsOffersAndRevokesOnSignout(t *testing.T) {
	s, ctx, cancel := managedTransferFixture(t)
	raw, _ := json.Marshal(transferScope{Owner: "owner", RuntimeID: "owned"})
	var paths []string
	for range 32 {
		offer, err := s.openTransfer(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, offer.(transferOffer).Socket)
	}
	if _, err := s.openTransfer(ctx, raw); err == nil {
		t.Fatal("unbounded local offers")
	}
	cancel()
	if _, err := s.openTransfer(context.Background(), raw); err == nil {
		t.Fatal("signed-out transfer accepted")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		remaining := 0
		for _, path := range paths {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				remaining++
			}
		}
		if remaining == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("signout retained transfer offers")
}
