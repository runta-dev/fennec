package terminalstream

import (
	"context"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSuspensionRetainsConnectionButReleasesConsumers(t *testing.T) {
	frames := make(chan Frame, 10)
	transport := healthFixture(t, func(server net.Conn) {
		decoder, encoder := json.NewDecoder(server), json.NewEncoder(server)
		for {
			var frame Frame
			if decoder.Decode(&frame) != nil {
				return
			}
			frames <- frame
			if frame.Type == "ping" {
				_ = encoder.Encode(Frame{Type: "pong", ID: frame.ID})
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	opening := Frame{Type: "attach", ID: "brisk-falcon", Terminal: &cloudruntime.LocalTerminal{ID: "brisk-falcon", Address: "t1"}}
	session, err := transport.Launch(opening)
	if err != nil {
		t.Fatal(err)
	}
	if frame := <-frames; frame.Type != "attach" {
		t.Fatal(frame)
	}
	transport.Suspend()
	if _, err := session.Next(ctx); err == nil {
		t.Fatal("local attachment did not enter reconnection")
	}
	session.Close()
	if transport.IsReady() || !transport.Alive() {
		t.Fatal("suspension destroyed the connection or advertised readiness")
	}
	if _, err := transport.Launch(opening); err == nil {
		t.Fatal("opened a session while offline")
	}
	select {
	case frame := <-frames:
		t.Fatalf("wrote into an offline TCP connection: %s", frame.Type)
	case <-time.After(20 * time.Millisecond):
	}
	if err := transport.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if !transport.IsReady() {
		t.Fatal("verified surviving connection not available")
	}
	for _, kind := range []string{"close", "ping"} {
		if frame := <-frames; frame.Type != kind {
			t.Fatalf("expected %s, got %s", kind, frame.Type)
		}
	}
	if _, err := transport.Launch(opening); err != nil {
		t.Fatalf("could not reattach over the same connection: %v", err)
	}
}

func healthFixture(t *testing.T, handle func(net.Conn)) *Transport {
	t.Helper()
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_ = json.NewEncoder(server).Encode(Frame{Type: "ready", Version: ProtocolVersion, Capabilities: []string{"probe_id"}})
		handle(server)
	}()
	transport := StartPipes(client, client)
	t.Cleanup(transport.Abort)
	return transport
}

func TestHealthProbeConfirmsPongWithoutLaunchingAgent(t *testing.T) {
	var count atomic.Int32
	transport := healthFixture(t, func(server net.Conn) {
		decoder, encoder := json.NewDecoder(server), json.NewEncoder(server)
		for {
			var frame Frame
			if decoder.Decode(&frame) != nil {
				return
			}
			if frame.Type != "ping" {
				t.Errorf("unexpected request: %s", frame.Type)
				return
			}
			count.Add(1)
			time.Sleep(40 * time.Millisecond)
			if encoder.Encode(Frame{Type: "pong", ID: frame.ID}) != nil {
				return
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := transport.Probe(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if count.Load() != 1 {
		t.Fatalf("concurrent probes sent %d pings", count.Load())
	}
}

func TestResumeRejectsPongFromBeforeOutage(t *testing.T) {
	staleSent, confirm := make(chan struct{}), make(chan struct{})
	transport := healthFixture(t, func(server net.Conn) {
		decoder, encoder := json.NewDecoder(server), json.NewEncoder(server)
		var before, after Frame
		if decoder.Decode(&before) != nil || decoder.Decode(&after) != nil {
			return
		}
		if before.ID == "" || before.ID == after.ID {
			t.Error("probe IDs were reused")
			return
		}
		if encoder.Encode(Frame{Type: "pong", ID: before.ID}) != nil {
			return
		}
		close(staleSent)
		<-confirm
		_ = encoder.Encode(Frame{Type: "pong", ID: after.ID})
		io.Copy(io.Discard, server)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if transport.Probe(ctx) == nil {
		cancel()
		t.Fatal("unanswered pre-outage probe accepted")
	}
	cancel()
	transport.Suspend()
	resume, cancelResume := context.WithTimeout(context.Background(), time.Second)
	defer cancelResume()
	done := make(chan error, 1)
	go func() { done <- transport.Resume(resume) }()
	select {
	case <-staleSent:
	case <-resume.Done():
		close(confirm)
		t.Fatal("missing stale response")
	}
	select {
	case err := <-done:
		close(confirm)
		t.Fatal("stale pong confirmed recovery", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(confirm)
	if err := <-done; err != nil {
		t.Fatal("matching fresh response rejected", err)
	}
}

func TestLegacyPeerDoesNotConfirmResumeWithUnnumberedPong(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		encoder, decoder := json.NewEncoder(server), json.NewDecoder(server)
		encoder.Encode(Frame{Type: "ready", Version: ProtocolVersion})
		for {
			var frame Frame
			if decoder.Decode(&frame) != nil {
				return
			}
			if frame.Type == "ping" {
				encoder.Encode(Frame{Type: "pong"})
			}
		}
	}()
	transport := StartPipes(client, client)
	defer transport.Abort()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := transport.Probe(ctx); err != nil {
		t.Fatal("legacy heartbeat compatibility lost", err)
	}
	transport.Suspend()
	if transport.Resume(ctx) == nil || transport.IsReady() {
		t.Fatal("legacy pong accepted as fresh recovery proof")
	}
}

func TestOldHeartbeatCannotRetireSuccessfullyResumedTransport(t *testing.T) {
	pings := make(chan Frame, 8)
	transport := healthFixture(t, func(server net.Conn) {
		decoder, encoder := json.NewDecoder(server), json.NewEncoder(server)
		first := true
		for {
			var frame Frame
			if decoder.Decode(&frame) != nil {
				return
			}
			if frame.Type != "ping" {
				continue
			}
			pings <- frame
			if first {
				first = false
				continue
			}
			if encoder.Encode(Frame{Type: "pong", ID: frame.ID}) != nil {
				return
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	select {
	case <-pings:
	case <-ctx.Done():
		t.Fatal("heartbeat did not start")
	}
	transport.Suspend()
	if err := transport.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	<-pings // The fresh recovery probe, not the old heartbeat.
	select {
	case <-pings: // The next heartbeat after the old deadline.
	case <-transport.Done():
		t.Fatal("old heartbeat retired a freshly verified connection")
	case <-ctx.Done():
		t.Fatal("heartbeat did not continue after recovery")
	}
	if !transport.IsReady() {
		t.Fatal("resumed connection lost readiness")
	}
}

func TestHealthProbeRejectsHalfOpenChannel(t *testing.T) {
	transport := healthFixture(t, func(server net.Conn) { io.Copy(io.Discard, server) })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := transport.Probe(ctx); err == nil {
		t.Fatal("blackholed connection accepted")
	}
	done := make(chan struct{})
	go func() { transport.Abort(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("abort blocked on dead channel")
	}
}

func TestHealthProbePreservesActiveLargeFrameTransfer(t *testing.T) {
	transport := healthFixture(t, func(server net.Conn) {
		go io.Copy(io.Discard, server)
		for {
			if _, err := server.Write([]byte(" ")); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := transport.Probe(ctx); err != nil {
		t.Fatalf("active transfer incorrectly retired: %v", err)
	}
}

func TestResumeRequiresPongDespiteBufferedTraffic(t *testing.T) {
	transport := healthFixture(t, func(server net.Conn) {
		go io.Copy(io.Discard, server)
		for {
			if _, err := server.Write([]byte(" ")); err != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	transport.Suspend()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := transport.Resume(ctx); err == nil || transport.IsReady() {
		t.Fatal("buffered traffic was mistaken for an acknowledged recovery probe")
	}
}
