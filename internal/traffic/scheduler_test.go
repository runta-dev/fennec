package traffic

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestContinuousTinyInteractivePacketsCannotStarveBulk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := scheduler(t, ctx, Config{BytesPerSecond: 1000, BulkBytesPerSecond: 1000, BurstBytes: 100, MaxPacketBytes: 100, QueuePackets: 8})
	if err := s.Write(ctx, "initial", Interactive, 100, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	bulk := make(chan error, 1)
	go func() { bulk <- s.Write(ctx, "file", Bulk, 100, func() error { return nil }) }()
	waitQueued(t, s, 1)
	interactiveDone := make(chan struct{})
	go func() {
		defer close(interactiveDone)
		for ctx.Err() == nil {
			if s.Write(ctx, "typing", Interactive, 1, func() error { return nil }) != nil {
				return
			}
		}
	}()
	select {
	case err := <-bulk:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("tiny interactive writes starved a bulk packet")
	}
	cancel()
	<-interactiveDone
}

func TestBulkQueueLeavesRoomForInteractiveAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := scheduler(t, ctx, Config{BytesPerSecond: 10000, BulkBytesPerSecond: 1, BurstBytes: 100, MaxPacketBytes: 100, QueuePackets: 8})
	if err := s.Write(ctx, "initial", Bulk, 100, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 20 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.Write(ctx, "file", Bulk, 100, func() error { return nil })
		}()
	}
	waitQueued(t, s, 7)
	ping, stop := context.WithTimeout(ctx, 250*time.Millisecond)
	defer stop()
	if err := s.Write(ping, "ping", Interactive, 10, func() error { return nil }); err != nil {
		t.Errorf("bulk prevented interactive admission: %v", err)
	}
	cancel()
	workers.Wait()
}

type sizedWriter struct {
	bytes.Buffer
	maximum int
}

func (w *sizedWriter) Write(p []byte) (int, error) {
	w.maximum = max(w.maximum, len(p))
	return w.Buffer.Write(p)
}

func TestWriterSplitsLargeTransferWithoutChangingBytes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := scheduler(t, ctx, DefaultConfig())
	var out sizedWriter
	w := Writer{Scheduler: s, Context: ctx, Flow: "file", Class: Bulk, Output: &out, Chunk: 1024, Overhead: 96}
	input := bytes.Repeat([]byte{0, 1, 255, '\n'}, 8192)
	n, err := w.Write(input)
	if err != nil || n != len(input) || !bytes.Equal(input, out.Bytes()) || out.maximum != 1024 {
		t.Fatalf("transfer: n=%d err=%v max=%d equal=%v", n, err, out.maximum, bytes.Equal(input, out.Bytes()))
	}
}

func scheduler(t *testing.T, ctx context.Context, config Config) *Scheduler {
	t.Helper()
	s, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func waitQueued(t *testing.T, s *Scheduler, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := len(s.queue)
		s.mu.Unlock()
		if n == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("packets did not reach the scheduler")
}

func TestInteractiveInterruptsBulkTokenWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := scheduler(t, ctx, Config{BytesPerSecond: 10000, BulkBytesPerSecond: 100, BurstBytes: 100, MaxPacketBytes: 100, QueuePackets: 8})
	if err := s.Write(ctx, "file", Bulk, 100, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	bulk := make(chan error, 1)
	go func() {
		bulk <- s.Write(ctx, "file", Bulk, 100, func() error { t.Error("bulk escaped its one-second token wait"); return nil })
	}()
	waitQueued(t, s, 1)
	start := time.Now()
	if err := s.Write(ctx, "ping", Interactive, 10, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("interactive packet waited behind bulk tokens: %s", elapsed)
	}
	cancel()
	if err := <-bulk; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued bulk cancellation: %v", err)
	}
}

func TestBothClassesRespectTotalAndBulkBudgets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	config := Config{BytesPerSecond: 2000, BulkBytesPerSecond: 1000, BurstBytes: 100, MaxPacketBytes: 100, QueuePackets: 8}
	s := scheduler(t, ctx, config)
	type sample struct {
		at    time.Time
		class Class
	}
	start := time.Now()
	var samples []sample
	var mu sync.Mutex
	var workers sync.WaitGroup
	for _, class := range []Class{Interactive, Bulk} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 12 {
				err := s.Write(ctx, string(rune('a'+class)), class, 100, func() error {
					mu.Lock()
					samples = append(samples, sample{time.Now(), class})
					mu.Unlock()
					return nil
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	workers.Wait()
	if len(samples) != 24 {
		t.Fatalf("only %d packets completed", len(samples))
	}
	for offset := 0; offset < len(samples); offset++ {
		from := start
		if offset > 0 {
			from = samples[offset-1].at
		}
		var total, bulk int
		for _, item := range samples[offset:] {
			total += 100
			if item.class == Bulk {
				bulk += 100
			}
			elapsed := item.at.Sub(from).Seconds()
			// Allow 2 ms of callback scheduling skew relative to token debit.
			if float64(total) > float64(config.BurstBytes)+(elapsed+0.002)*float64(config.BytesPerSecond) {
				t.Fatalf("root budget exceeded: %d bytes in %.4fs", total, elapsed)
			}
			if float64(bulk) > float64(config.BurstBytes)+(elapsed+0.002)*float64(config.BulkBytesPerSecond) {
				t.Fatalf("bulk budget exceeded: %d bytes in %.4fs", bulk, elapsed)
			}
		}
	}
}

func TestPriorityPreservesFlowOrderAndVisitsOtherFlows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := scheduler(t, ctx, DefaultConfig())
	started, release := make(chan struct{}), make(chan struct{})
	var order []string
	var workers sync.WaitGroup
	queue := func(flow, label string, class Class, write func()) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := s.Write(ctx, flow, class, 1, func() error {
				if write != nil {
					write()
				}
				order = append(order, label)
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	queue("first", "first", Bulk, func() { close(started); <-release })
	<-started
	queue("file", "file-data", Bulk, nil)
	waitQueued(t, s, 1)
	queue("file", "file-eof", Interactive, nil)
	waitQueued(t, s, 2)
	queue("file", "file-last", Bulk, nil)
	waitQueued(t, s, 3)
	queue("peer", "peer-data", Bulk, nil)
	waitQueued(t, s, 4)
	queue("ping", "pong", Interactive, nil)
	waitQueued(t, s, 5)
	close(release)
	workers.Wait()
	want := []string{"first", "pong", "file-data", "file-eof", "peer-data", "file-last"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestCancellationDoesNotReleaseRunningBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := scheduler(t, ctx, DefaultConfig())
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Write(ctx, "file", Bulk, 100, func() error { close(started); <-release; return ctx.Err() })
	}()
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("Write returned while the callback still owned its buffer")
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestOversizedPacketIsRejectedInsteadOfWaitingForever(t *testing.T) {
	s := scheduler(t, context.Background(), DefaultConfig())
	err := s.Write(context.Background(), "file", Bulk, s.config.MaxPacketBytes+1, func() error {
		t.Fatal("oversized atomic write reached transport")
		return nil
	})
	if !errors.Is(err, ErrPacketSize) {
		t.Fatal(err)
	}
}
