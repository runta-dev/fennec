package cli

import (
	"fennec/internal/connectivity"
	"testing"
	"time"
)

func TestChannelReportsUnreachableOnlyAfterAPersistentOutage(t *testing.T) {
	now := time.Now()
	channel := &terminalChannel{}
	if channel.unreachable(now) {
		t.Fatal("a channel that never lost a connection is unreachable")
	}
	channel.downSince = now.Add(-unreachableAfter / 2)
	if channel.unreachable(now) {
		t.Fatal("a brief reconnect was reported as unreachable")
	}
	channel.downSince = now.Add(-unreachableAfter)
	if !channel.unreachable(now) {
		t.Fatal("a persistent outage was not reported")
	}
	// An offline Mac is reported as offline instead.
	online := false
	channel.network = connectivity.NewMonitor(func() bool { return online }, time.Hour)
	channel.network.Refresh()
	if channel.unreachable(now) {
		t.Fatal("an offline Mac reported its computer unreachable")
	}
}
