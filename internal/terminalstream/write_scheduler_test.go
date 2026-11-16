package terminalstream

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestQueuedHeartbeatPassesBulkWithoutReorderingSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := newWriteScheduler(ctx)
	blocked, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var output []string
	var workers sync.WaitGroup
	queue := func(id, label string, urgent bool, block bool) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := s.write(id, urgent, func() error {
				if block {
					close(blocked)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				mu.Lock()
				output = append(output, label)
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	queue("first", "first", false, true)
	<-blocked
	waitQueued := func(n int) {
		t.Helper()
		for ctx.Err() == nil {
			s.mu.Lock()
			count := len(s.queue)
			s.mu.Unlock()
			if count == n {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("writer did not enqueue")
	}
	queue("bulk", "bulk-data", false, false)
	waitQueued(1)
	queue("bulk", "bulk-credit", true, false)
	waitQueued(2)
	queue("peer", "peer-data", false, false)
	waitQueued(3)
	queue("heartbeat", "pong", true, false)
	waitQueued(4)
	close(release)
	workers.Wait()
	if want := []string{"first", "pong", "bulk-data", "bulk-credit", "peer-data"}; !reflect.DeepEqual(output, want) {
		t.Fatalf("wire order %v, want %v", output, want)
	}
}

func TestCancelledWriterReleasesQueuedFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newWriteScheduler(ctx)
	started := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		done <- s.write("bulk", false, func() error { close(started); <-ctx.Done(); return ctx.Err() })
	}()
	<-started
	go func() {
		done <- s.write("ping", true, func() error { t.Error("canceled frame was written"); return nil })
	}()
	cancel()
	for range 2 {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("queued write leaked")
		}
	}
}
