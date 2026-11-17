package traffic

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

func TestSchedulerUpdateConfigAppliesNewRate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := Config{BytesPerSecond: 2000, BulkBytesPerSecond: 1000, BurstBytes: 100, MaxPacketBytes: 100, QueuePackets: 8}
	s := scheduler(t, ctx, config)
	updated := config
	updated.BytesPerSecond = 4000
	updated.BulkBytesPerSecond = 3000
	if err := s.UpdateConfig(updated); err != nil {
		t.Fatal(err)
	}
	if s.root.rate != 4000 || s.bulk.rate != 3000 {
		t.Fatalf("rates: %g %g", s.root.rate, s.bulk.rate)
	}
	updated.BulkBytesPerSecond = 5000
	if err := s.UpdateConfig(updated); err == nil {
		t.Fatal("bulk above root accepted")
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.BurstBytes++ }, func(c *Config) { c.MaxPacketBytes-- }, func(c *Config) { c.QueuePackets++ }} {
		invalid := config
		mutate(&invalid)
		if err := s.UpdateConfig(invalid); err == nil {
			t.Fatal("mutable structural limit accepted")
		}
	}
}

func TestRetryPreservesConcurrentRateUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := scheduler(t, ctx, DefaultConfig())
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- s.TryWrite(ctx, "blocked", Bulk, 100, func() error {
			calls++
			if calls == 1 {
				close(started)
				<-release
				return ErrWouldBlock
			}
			return nil
		})
	}()
	<-started
	cfg := DefaultConfig()
	cfg.BytesPerSecond *= 2
	cfg.BulkBytesPerSecond *= 2
	err := s.UpdateConfig(cfg)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root.rate != float64(cfg.BytesPerSecond) || s.bulk.rate != float64(cfg.BulkBytesPerSecond) {
		t.Fatalf("retry undid new rates: %g %g", s.root.rate, s.bulk.rate)
	}
	if s.root.tokens > s.root.burst || s.bulk.tokens > s.bulk.burst {
		t.Fatal("refund minted excess tokens")
	}
}

func TestConcurrentUpdatesAndWriterAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := scheduler(t, ctx, DefaultConfig())
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for i := 0; i < 1000; i++ {
			cfg := DefaultConfig()
			cfg.BytesPerSecond += i
			if err := s.UpdateConfig(cfg); err != nil {
				t.Error(err)
			}
		}
	}()
	w := Writer{Scheduler: s, Context: ctx, Flow: "interactive", Class: Interactive, Output: io.Discard, Chunk: 1}
	for range 1000 {
		if _, err := w.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	workers.Wait()
}

func TestUpdateWakesTokenWaiter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfg := Config{BytesPerSecond: 1, BulkBytesPerSecond: 1, BurstBytes: 100, MaxPacketBytes: 100, QueuePackets: 8}
	s := scheduler(t, ctx, cfg)
	if err := s.Write(ctx, "initial", Bulk, 100, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Write(ctx, "bulk", Bulk, 100, func() error { return nil }) }()
	waitQueued(t, s, 1)
	cfg.BytesPerSecond = 100000
	cfg.BulkBytesPerSecond = 100000
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("rate update did not wake old token wait")
	}
}

func TestUpdateDoesNotRefillPastTimeAtNewRate(t *testing.T) {
	cfg := Config{BytesPerSecond: 100, BulkBytesPerSecond: 50, BurstBytes: 10000, MaxPacketBytes: 100, QueuePackets: 8}
	s := scheduler(t, context.Background(), cfg)
	s.mu.Lock()
	s.root.tokens = 0
	s.bulk.tokens = 0
	s.root.updated = time.Now().Add(-time.Second)
	s.bulk.updated = s.root.updated
	s.mu.Unlock()
	cfg.BytesPerSecond = 100000
	cfg.BulkBytesPerSecond = 50000
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root.tokens > 200 || s.bulk.tokens > 100 {
		t.Fatalf("retroactive refill: %g %g", s.root.tokens, s.bulk.tokens)
	}
}
