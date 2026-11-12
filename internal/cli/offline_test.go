package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/connectivity"
	"fennec/internal/controlplane"
	"fennec/internal/launcher"
	"sync/atomic"
	"testing"
	"time"
)

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
