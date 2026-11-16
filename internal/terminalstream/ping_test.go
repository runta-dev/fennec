package terminalstream

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestMeasuredPingsMatchIndependentReplies(t *testing.T) {
	firstRead := make(chan Frame, 1)
	replyFirst := make(chan struct{})
	transport := healthFixture(t, func(server net.Conn) {
		decoder, encoder := json.NewDecoder(server), json.NewEncoder(server)
		var first, second Frame
		if decoder.Decode(&first) != nil {
			return
		}
		firstRead <- first
		if decoder.Decode(&second) != nil {
			return
		}
		if first.ID == second.ID || !strings.HasPrefix(second.ID, "measure-") {
			t.Error("measurements shared an ID")
		}
		_ = encoder.Encode(Frame{Type: "pong", ID: second.ID})
		<-replyFirst
		_ = encoder.Encode(Frame{Type: "pong", ID: first.ID})
		var end Frame
		_ = decoder.Decode(&end)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	firstDone := make(chan error, 1)
	go func() { _, err := transport.Ping(ctx); firstDone <- err }()
	select {
	case <-firstRead:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if elapsed, err := transport.Ping(ctx); err != nil || elapsed <= 0 {
		t.Fatalf("second measurement: %s, %v", elapsed, err)
	}
	select {
	case err := <-firstDone:
		t.Fatalf("wrong pong completed first measurement: %v", err)
	default:
	}
	close(replyFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestMeasuredPingRejectsUnrelatedTrafficAndLatePong(t *testing.T) {
	transport := healthFixture(t, func(server net.Conn) {
		decoder, encoder := json.NewDecoder(server), json.NewEncoder(server)
		var first, second Frame
		if decoder.Decode(&first) != nil {
			return
		}
		_ = encoder.Encode(Frame{Type: "pong", ID: "unrelated"})
		_ = encoder.Encode(Frame{Type: "data", ID: "other-stream", Data: []byte("still alive")})
		if decoder.Decode(&second) != nil {
			return
		}
		_ = encoder.Encode(Frame{Type: "pong", ID: first.ID})
		_ = encoder.Encode(Frame{Type: "pong", ID: second.ID})
		var end Frame
		_ = decoder.Decode(&end)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if elapsed, err := transport.Ping(ctx); !errors.Is(err, context.DeadlineExceeded) || elapsed != 0 {
		t.Fatalf("unmatched traffic confirmed a measurement: %s, %v", elapsed, err)
	}
	ctx, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if _, err := transport.Ping(ctx); err != nil {
		t.Fatalf("timeout damaged shared transport: %v", err)
	}
}

func TestMeasuredPingBoundsBlockedWrites(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	transport := healthFixture(t, func(server net.Conn) { <-release })
	ready, cancelReady := context.WithTimeout(context.Background(), time.Second)
	defer cancelReady()
	if err := transport.Ready(ready); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := transport.Ping(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}
	if _, err := transport.Ping(ready); !errors.Is(err, ErrPingBusy) {
		t.Fatalf("blocked writes exceeded admission budget: %v", err)
	}
}
