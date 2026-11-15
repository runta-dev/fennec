package localssh

import (
	"context"
	"sync"
	"testing"
	"time"
)

type relayNetworkFixture struct {
	mu       sync.Mutex
	online   bool
	changed  chan struct{}
	observed chan bool
}

func (n *relayNetworkFixture) Online() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.observed <- n.online
	return n.online
}
func (n *relayNetworkFixture) Changes() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.changed
}
func (n *relayNetworkFixture) update(online bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.online = online
	close(n.changed)
	n.changed = make(chan struct{})
}

func TestRelayKeepsHealthyConnectionAcrossOnlineNotifications(t *testing.T) {
	network := &relayNetworkFixture{online: true, changed: make(chan struct{}), observed: make(chan bool, 32)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); cancelOfflineRelay(ctx, network, cancel) }()
	wait := func() {
		t.Helper()
		select {
		case <-network.observed:
		case <-time.After(time.Second):
			t.Fatal("network event not processed")
		}
	}
	wait()
	for i := 0; i < 8; i++ {
		network.update(true)
		wait()
		if ctx.Err() != nil {
			t.Fatal("online event canceled healthy relay")
		}
	}
	network.update(false)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("offline relay was not canceled")
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("offline bridge remained active")
	}
}
