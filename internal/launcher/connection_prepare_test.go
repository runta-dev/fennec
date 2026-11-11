package launcher

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandshakeOverlapNeverSkipsAuthorization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	handshake := make(chan struct{})
	allow := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- prepareConnection(ctx, func(ctx context.Context) error {
			select {
			case <-allow:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, func(context.Context) error { close(handshake); return nil }, func() bool { return true }, func() { t.Error("successful connection stopped") })
	}()
	<-handshake
	select {
	case <-result:
		t.Fatal("handshake exposed before authorization")
	default:
	}
	close(allow)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
func TestAuthorizationFailureClosesSpeculativeHandshake(t *testing.T) {
	denied := errors.New("not owner")
	var stopped atomic.Bool
	err := prepareConnection(context.Background(), func(context.Context) error { return denied }, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, func() bool { return true }, func() { stopped.Store(true) })
	if !errors.Is(err, denied) || !stopped.Load() {
		t.Fatal(err, stopped.Load())
	}
}
func TestChangedIdentityRehandshakesAfterAuthorization(t *testing.T) {
	var calls atomic.Int32
	var stopped atomic.Bool
	err := prepareConnection(context.Background(), func(context.Context) error { return nil }, func(context.Context) error { calls.Add(1); return nil }, func() bool { return false }, func() { stopped.Store(true) })
	if err != nil || calls.Load() != 2 || !stopped.Load() {
		t.Fatal(err, calls.Load(), stopped.Load())
	}
}
