package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/connectivity"
	"fennec/internal/controlplane"
	"fennec/internal/launcher"
	"fennec/internal/terminalstream"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestRoutingBurstDoesNotDestroySuspendedTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var online, respond atomic.Bool
	online.Store(true)
	network := connectivity.NewMonitor(online.Load, time.Hour)
	network.Start(ctx)
	client, peer := net.Pipe()
	probed := make(chan struct{}, 8)
	go func() {
		defer peer.Close()
		encoder, decoder := json.NewEncoder(peer), json.NewDecoder(peer)
		encoder.Encode(terminalstream.Frame{Type: "ready", Version: terminalstream.ProtocolVersion, Capabilities: []string{"probe_id"}})
		for {
			var frame terminalstream.Frame
			if decoder.Decode(&frame) != nil {
				return
			}
			if frame.Type == "ping" {
				select {
				case probed <- struct{}{}:
				default:
				}
				if respond.Load() {
					encoder.Encode(terminalstream.Frame{Type: "pong", ID: frame.ID})
				}
			}
		}
	}()
	transport := terminalstream.StartPipes(client, client)
	defer transport.Abort()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	transport.Suspend()
	channel := &terminalChannel{ctx: ctx, network: network, transport: transport, connect: func(ctx context.Context) (*ssh.Client, error) { <-ctx.Done(); return nil, ctx.Err() }}
	first := make(chan error, 1)
	go func() { _, err := channel.get(ctx); first <- err }()
	select {
	case <-probed:
	case <-ctx.Done():
		t.Fatal("probe did not start")
	}
	online.Store(false)
	network.Refresh()
	select {
	case err := <-first:
		if err == nil {
			t.Fatal("canceled recovery reported ready")
		}
	case <-ctx.Done():
		t.Fatal("recovery did not cancel")
	}
	if !transport.Alive() || !transport.Suspended() {
		t.Fatal("routing event destroyed retained connection")
	}
	respond.Store(true)
	online.Store(true)
	network.Refresh()
	got, err := channel.get(ctx)
	if err != nil || got != transport || !transport.IsReady() {
		t.Fatalf("next route failed to reuse transport: %v", err)
	}
}

func TestOfflineRecoveryReusesSurvivingTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var online atomic.Bool
	network := connectivity.NewMonitor(online.Load, time.Millisecond)
	network.Start(ctx)
	client, peer := net.Pipe()
	go func() {
		defer peer.Close()
		encoder, decoder := json.NewEncoder(peer), json.NewDecoder(peer)
		_ = encoder.Encode(terminalstream.Frame{Type: "ready", Version: terminalstream.ProtocolVersion, Capabilities: []string{"probe_id"}})
		for {
			var frame terminalstream.Frame
			if decoder.Decode(&frame) != nil {
				return
			}
			if frame.Type == "ping" {
				_ = encoder.Encode(terminalstream.Frame{Type: "pong", ID: frame.ID})
			}
		}
	}()
	transport := terminalstream.StartPipes(client, client)
	defer transport.Abort()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	channel := &terminalChannel{ctx: ctx, network: network, transport: transport, generation: 1}
	if _, err := channel.get(ctx); !errors.Is(err, connectivity.ErrOffline) {
		t.Fatalf("offline get: %v", err)
	}
	if !transport.Alive() || transport.IsReady() {
		t.Fatal("offline detection discarded SSH or claimed readiness")
	}
	done := channel.start()
	online.Store(true)
	network.Refresh()
	for !transport.IsReady() {
		select {
		case <-ctx.Done():
			t.Fatal("failed to probe and reuse the surviving transport")
		case <-time.After(time.Millisecond):
		}
	}
	got, err := channel.get(ctx)
	if err != nil || got != transport {
		t.Fatalf("replaced reusable transport: %v", err)
	}
	cancel()
	<-done
}

func TestOfflineSessionServicePreservesSessionsAndSkipsRetries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var online atomic.Bool
	online.Store(true)
	network := connectivity.NewMonitor(online.Load, 10*time.Millisecond)
	network.Start(ctx)

	var listCalls atomic.Int64
	const runtimeID = "01a1b144-56c1-7fc2-a4ab-cf8d84f8543e"
	service := &fenSessionService{
		network:   network,
		runtimeID: runtimeID,
		refresh:   make(chan struct{}, 1),
		list: func(context.Context) ([]byte, error) {
			listCalls.Add(1)
			if !online.Load() {
				return nil, connectivity.ErrOffline
			}
			return []byte(`{"terminals":[{"address":"t1","state":"live","title":"Task","labels":{"fennec.session":"alpha-bravo","fennec.agent":"codex"}}]}`), nil
		},
	}

	service.update(ctx)
	if listCalls.Load() != 1 || len(service.sessions) != 1 || service.problem != "" {
		t.Fatalf("initial online update failed: calls=%d sessions=%d problem=%q", listCalls.Load(), len(service.sessions), service.problem)
	}

	online.Store(false)
	network.Refresh()
	service.update(ctx)
	if listCalls.Load() != 1 {
		t.Fatalf("offline update should not query remote transport: calls=%d", listCalls.Load())
	}
	if len(service.sessions) != 1 || service.problem != "" {
		t.Fatalf("offline update lost cached sessions or set error: sessions=%d problem=%q", len(service.sessions), service.problem)
	}

	channel := &terminalChannel{ctx: ctx, network: network, service: service}
	if _, err := channel.get(ctx); !errors.Is(err, connectivity.ErrOffline) {
		t.Fatalf("expected ErrOffline from terminalChannel.get while offline, got %v", err)
	}

	shared, err := launcher.NewSharedConnection(ctx, t.TempDir(), "01a1b144-56c1-7fc2-a4ab-cf8d84f8543e", func(context.Context) error {
		t.Fatal("authorize should not run while offline")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	shared.Online = network.Online
	shared.WaitOnline = network.WaitOnline
	shared.Changes = network.Changes
	if _, err := shared.Get(ctx); !errors.Is(err, connectivity.ErrOffline) {
		t.Fatalf("expected ErrOffline from SharedConnection.Get while offline, got %v", err)
	}

	// Daemon sessions.refresh returns cached listing cleanly while offline instead of refresh_failed.
	daemon := &daemonServices{
		network:     network,
		channel:     channel,
		brokerReady: true,
		config:      brokerConfiguration{RuntimeID: runtimeID},
	}
	result, err := daemon.session(ctx, "tok", runtimeID, "refresh", controlplane.SessionParams{})
	if err != nil {
		t.Fatalf("offline sessions.refresh returned error: %v", err)
	}
	raw, ok := result.(json.RawMessage)
	if !ok || len(raw) == 0 {
		t.Fatalf("expected cached raw listing while offline, got %T %s", result, raw)
	}
}
