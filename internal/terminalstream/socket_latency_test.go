package terminalstream

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type delayedPacket struct {
	data    []byte
	deliver time.Time
}
type delayedWriter struct {
	output io.WriteCloser
	queue  chan delayedPacket
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	delay  time.Duration
}

func newDelayedWriter(parent context.Context, output io.WriteCloser, delay time.Duration) *delayedWriter {
	ctx, cancel := context.WithCancel(parent)
	w := &delayedWriter{output: output, queue: make(chan delayedPacket, 64), ctx: ctx, cancel: cancel, done: make(chan struct{}), delay: delay}
	context.AfterFunc(ctx, func() { output.Close() })
	go func() {
		defer close(w.done)
		for {
			var packet delayedPacket
			select {
			case <-ctx.Done():
				return
			case packet = <-w.queue:
			}
			timer := time.NewTimer(max(0, time.Until(packet.deliver)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if _, err := output.Write(packet.data); err != nil {
				return
			}
		}
	}()
	return w
}
func (w *delayedWriter) Write(data []byte) (int, error) {
	packet := delayedPacket{append([]byte(nil), data...), time.Now().Add(w.delay)}
	select {
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	case w.queue <- packet:
		return len(data), nil
	}
}
func (w *delayedWriter) Close() error { w.cancel(); return w.output.Close() }

// A propagation delay is applied asynchronously to each queued packet, not as
// a sleep inside Write. Otherwise small writes would incorrectly reduce link
// bandwidth even with an adequate credit window.
func TestSocketBulkUsesBandwidthAcrossLatencyWithoutBlockingPing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	uplink := newDelayedWriter(ctx, send, 90*time.Millisecond)
	downlink := newDelayedWriter(ctx, output, 90*time.Millisecond)
	server := make(chan error, 1)
	go func() {
		server <- ServeWithSockets(ctx, input, downlink, nil, nil, nil, nil, func(ctx context.Context, _ string) (net.Conn, error) {
			local, remote := net.Pipe()
			context.AfterFunc(ctx, func() { remote.Close() })
			go func() { defer remote.Close(); io.Copy(remote, remote) }()
			return local, nil
		})
	}()
	transport := StartPipes(uplink, receive)
	defer func() { transport.Close(); cancel(); <-server; <-uplink.done; <-downlink.done }()
	conn, err := transport.DialSocket(ctx, SocketDeviceRelay, "1234abcd")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := bytes.Repeat([]byte("0123456789abcdef"), 8192)
	conn.SetDeadline(time.Now().Add(4 * time.Second))
	started := time.Now()
	var transfer sync.WaitGroup
	transfer.Add(1)
	written := make(chan error, 1)
	go func() { _, err := conn.Write(payload); written <- err }()
	received := make(chan error, 1)
	go func() {
		defer transfer.Done()
		got := make([]byte, len(payload))
		_, err := io.ReadFull(conn, got)
		if err == nil && !bytes.Equal(got, payload) {
			err = io.ErrUnexpectedEOF
		}
		received <- err
	}()
	for range 6 {
		rtt, err := transport.Ping(ctx)
		if err != nil || rtt > 260*time.Millisecond {
			t.Fatalf("180 ms propagation ping delayed by bulk: %s %v", rtt, err)
		}
	}
	if err := <-received; err != nil {
		t.Fatal("bulk could not use the link within 4 seconds", err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	transfer.Wait()
	t.Logf("verified %d bytes in each direction in %s at 180 ms propagation RTT", len(payload), time.Since(started))
}
