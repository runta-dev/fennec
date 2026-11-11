package cli

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConfigurationInputsOverlapButWaitForBoth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 1)
	job := func(ctx context.Context) error {
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	go func() { done <- gatherConfigurationInputs(ctx, job, job) }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("independent reads were serialized")
		}
	}
	select {
	case <-done:
		t.Fatal("configuration proceeded before reads completed")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestConfigurationInputFailureCancelsAndDrainsSibling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	failed := errors.New("fixture read failure")
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- gatherConfigurationInputs(ctx, func(ctx context.Context) error {
			select {
			case <-started:
				return failed
			case <-ctx.Done():
				return ctx.Err()
			}
		}, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(canceled)
			select {
			case <-release:
			case <-time.After(time.Second):
			}
			return ctx.Err()
		})
	}()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("sibling was not canceled")
	}
	select {
	case <-done:
		t.Fatal("returned before canceled read drained")
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, failed) {
		t.Fatal("read failure was lost", err)
	}
}
func TestCanceledConfigurationCannotProceed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gatherConfigurationInputs(ctx, func(context.Context) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
