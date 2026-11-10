package launcher

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSharedConnectionCoalescesInitializationAndReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	var healthy atomic.Bool
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	manager := &SharedConnection{ctx: ctx, control: "private-socket", initialize: func(context.Context) error {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		healthy.Store(true)
		return nil
	}, check: func(context.Context) error {
		if healthy.Load() {
			return nil
		}
		return errors.New("disconnected")
	}}
	var workers sync.WaitGroup
	for index := 0; index < 8; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			path, err := manager.Get(ctx)
			if err != nil || path != "private-socket" {
				t.Error(path, err)
			}
		}()
	}
	<-entered
	close(release)
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate initialization", calls.Load())
	}
	healthy.Store(false)
	if _, err := manager.Get(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("dead connection not rebuilt", calls.Load())
	}
}

func TestSharedConnectionCallerCancellationDoesNotCancelOtherWaiters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	manager := &SharedConnection{ctx: ctx, control: "private-socket", initialize: func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, check: func(context.Context) error { return nil }}
	caller, stopCaller := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { _, err := manager.Get(caller); result <- err }()
	<-entered
	stopCaller()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if _, err := manager.Get(ctx); err != nil {
		t.Fatal("one canceled caller stopped shared initialization", err)
	}
}

func TestSharedConnectionShutdownWaitsForCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	cleaned := make(chan struct{})
	manager := &SharedConnection{ctx: ctx, initialize: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }, check: func(context.Context) error { return errors.New("not connected") }, stop: func() { close(cleaned) }}
	done := manager.Start()
	<-entered
	cancel()
	<-done
	select {
	case <-cleaned:
	default:
		t.Fatal("shutdown returned before master cleanup")
	}
}
