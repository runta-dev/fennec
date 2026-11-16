package trafficbroker

import (
	"bytes"
	"context"
	"fennec/internal/traffic"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func fixture(t *testing.T, config traffic.Config) (context.Context, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fen-traffic-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "v1.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, config) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("broker did not stop")
		}
	})
	return ctx, path
}
func pipeClient(t *testing.T, ctx context.Context, path string) (*Client, *os.File) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { read.Close(); write.Close() })
	client, err := Open(ctx, path, write)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client, read
}
func TestSeparateClientsShareOneRateBudget(t *testing.T) {
	config := traffic.DefaultConfig()
	config.BytesPerSecond = 16 << 10
	config.BulkBytesPerSecond = 16 << 10
	ctx, path := fixture(t, config)
	const packets = 8
	var writers sync.WaitGroup
	errors := make(chan error, 4)
	start := time.Now()
	for i := 0; i < 2; i++ {
		client, read := pipeClient(t, ctx, path)
		data := bytes.Repeat([]byte{byte(i + 1)}, 1024)
		writers.Add(1)
		go func() {
			defer writers.Done()
			for range packets {
				if err := client.WritePacket(ctx, "data", traffic.Bulk, data); err != nil {
					errors <- err
					return
				}
			}
		}()
		go func() {
			got := make([]byte, len(data)*packets)
			_, err := io.ReadFull(read, got)
			if err == nil && !bytes.Equal(got, bytes.Repeat(data, packets)) {
				err = io.ErrUnexpectedEOF
			}
			errors <- err
		}()
	}
	writers.Wait()
	elapsed := time.Since(start)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	minimum := time.Duration(float64(2*packets*(1024+overhead)-config.BurstBytes) / float64(config.BytesPerSecond) * float64(time.Second))
	if elapsed < minimum-20*time.Millisecond {
		t.Fatalf("independent clients bypassed aggregate cap: %s < %s", elapsed, minimum)
	}
	if elapsed > minimum+time.Second {
		t.Fatalf("unexpected stalled throughput: %s", elapsed)
	}
}
func TestFullDestinationDoesNotBlockPeerAndDisconnectCancels(t *testing.T) {
	ctx, path := fixture(t, traffic.DefaultConfig())
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	fd := int(write.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := unix.Write(fd, make([]byte, 4096))
		if err == unix.EAGAIN {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	stalled, err := Open(ctx, path, write)
	if err != nil {
		t.Fatal(err)
	}
	defer stalled.Close()
	blocked := make(chan error, 1)
	go func() { blocked <- stalled.WritePacket(ctx, "file", traffic.Bulk, bytes.Repeat([]byte("x"), 1024)) }()
	peer, output := pipeClient(t, ctx, path)
	start := time.Now()
	if err := peer.WritePacket(ctx, "ping", traffic.Interactive, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	var got [4]byte
	if _, err := io.ReadFull(output, got[:]); err != nil {
		t.Fatal(err)
	}
	if string(got[:]) != "ping" || time.Since(start) > 100*time.Millisecond {
		t.Fatal("stalled file blocked the terminal")
	}
	stalled.Close()
	select {
	case err := <-blocked:
		if err == nil {
			t.Fatal("full pipe write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("disconnected client retained blocked packet")
	}
}
func TestInteractivePacketPassesBulkTokenWaitOnSameClient(t *testing.T) {
	config := traffic.DefaultConfig()
	config.BulkBytesPerSecond = 2048
	ctx, path := fixture(t, config)
	client, output := pipeClient(t, ctx, path)
	bulk := bytes.Repeat([]byte("b"), 1024)
	if err := client.WritePacket(ctx, "bulk", traffic.Bulk, bulk); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() { blocked <- client.WritePacket(ctx, "bulk", traffic.Bulk, bulk) }()
	// First packet leaves fewer tokens than the second one needs; wait until
	// the second call has entered the client to exercise multiplexed requests.
	limit := time.After(time.Second)
	for {
		client.mu.Lock()
		n := len(client.pending)
		client.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-limit:
			t.Fatal("bulk was not queued")
		case <-time.After(time.Millisecond):
		}
	}
	start := time.Now()
	if err := client.WritePacket(ctx, "ping", traffic.Interactive, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 70*time.Millisecond {
		t.Fatalf("interactive packet waited for bulk: %s", elapsed)
	}
	got := make([]byte, len(bulk)+4)
	if _, err := io.ReadFull(output, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append(append([]byte(nil), bulk...), []byte("ping")...)) {
		t.Fatal("interactive packet did not pass bulk")
	}
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
}
func TestBrokerRejectsRegularFiles(t *testing.T) {
	ctx, path := fixture(t, traffic.DefaultConfig())
	file, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	client, err := Open(ctx, path, file)
	if client != nil {
		client.Close()
	}
	if err == nil {
		t.Fatal("accepted a potentially blocking disk writer")
	}
}

func TestBrokerSingletonDoesNotReplaceLiveSocket(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fen-traffic-home-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, home) }()
	path := filepath.Join(directory(home), "out.sock")
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("broker failed to start")
		}
		time.Sleep(time.Millisecond)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, home); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("duplicate broker replaced live socket", err)
	}
	client, output := pipeClient(t, ctx, path)
	if err := client.WritePacket(ctx, "ping", traffic.Interactive, []byte("ok")); err != nil {
		t.Fatal(err)
	}
	var got [2]byte
	if _, err := io.ReadFull(output, got[:]); err != nil {
		t.Fatal(err)
	}
	client.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker did not stop")
	}
}

func TestSocketPacketsRemainWholeAcrossBackpressure(t *testing.T) {
	ctx, path := fixture(t, traffic.DefaultConfig())
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	unix.CloseOnExec(pair[0])
	unix.CloseOnExec(pair[1])
	if err := unix.SetsockoptInt(pair[1], unix.SOL_SOCKET, unix.SO_SNDBUF, 1024); err != nil {
		t.Fatal(err)
	}
	read, write := os.NewFile(uintptr(pair[0]), "read"), os.NewFile(uintptr(pair[1]), "write")
	defer read.Close()
	defer write.Close()
	client, err := Open(ctx, path, write)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	const count = 12
	completed := make(chan error, count)
	for i := range count {
		go func() {
			completed <- client.WritePacket(ctx, string(rune('a'+i)), traffic.Bulk, bytes.Repeat([]byte{byte(i + 1)}, maxData))
		}()
	}
	// Let the small socket buffer fill before resuming its reader.
	time.Sleep(100 * time.Millisecond)
	seen := map[byte]bool{}
	for range count {
		packet := make([]byte, maxData)
		if _, err := io.ReadFull(read, packet); err != nil {
			t.Fatal(err)
		}
		if packet[0] < 1 || int(packet[0]) > count || seen[packet[0]] || !bytes.Equal(packet, bytes.Repeat(packet[:1], maxData)) {
			t.Fatal("packet tail interleaved or content changed")
		}
		seen[packet[0]] = true
	}
	for range count {
		if err := <-completed; err != nil {
			t.Fatal(err)
		}
	}
}

func TestIdleBrokerRetainsConnectedClientThenExits(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "fen-traffic-idle-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "out.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listener, traffic.DefaultConfig(), 80*time.Millisecond) }()
	client, output := pipeClient(t, ctx, path)
	time.Sleep(200 * time.Millisecond)
	if err := client.WritePacket(ctx, "ping", traffic.Interactive, []byte("ok")); err != nil {
		t.Fatal("idle broker retired a connected client", err)
	}
	var got [2]byte
	if _, err := io.ReadFull(output, got[:]); err != nil {
		t.Fatal(err)
	}
	client.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle owner did not exit")
	}
}
