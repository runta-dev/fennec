package traffic

import (
	"context"
	"testing"
	"time"
)

func TestConfigForCapacityReservesInteractive(t *testing.T) {
	config := ConfigForCapacity(512 * 1024)
	if config.BytesPerSecond != 512*1024 {
		t.Fatalf("root budget: %d", config.BytesPerSecond)
	}
	if got, want := config.BulkBytesPerSecond, 512*1024-InteractiveReserveBytesPerSecond; got != want {
		t.Fatalf("bulk budget: %d, want %d", got, want)
	}
	small := ConfigForCapacity(40 * 1024)
	if small.BytesPerSecond != DefaultConfig().BytesPerSecond {
		t.Fatalf("small capacity below minimum changed root: %d", small.BytesPerSecond)
	}
}

func TestSchedulerUpdateConfigAppliesNewRate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := Config{BytesPerSecond: 2000, BulkBytesPerSecond: 1000, BurstBytes: 100, MaxPacketBytes: 100, QueuePackets: 8}
	s, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateConfig(Config{BytesPerSecond: 4000, BulkBytesPerSecond: 3000, BurstBytes: 100, MaxPacketBytes: 100, QueuePackets: 8}); err != nil {
		t.Fatal(err)
	}
	if s.root.rate != 4000 || s.bulk.rate != 3000 {
		t.Fatalf("updated rates: root=%f bulk=%f", s.root.rate, s.bulk.rate)
	}
	if err = s.UpdateConfig(Config{BytesPerSecond: 1000, BulkBytesPerSecond: 2000, BurstBytes: 100, MaxPacketBytes: 100, QueuePackets: 8}); err == nil {
		t.Fatal("bulk above root accepted")
	}
	_ = time.Now
}
