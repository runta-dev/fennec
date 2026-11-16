package trafficbroker

import (
	"bytes"
	"context"
	"fennec/internal/traffic"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func congestedTCP(t *testing.T) (*os.File, *net.TCPConn) {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	sender, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	receiver, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { receiver.Close() })
	if err := receiver.SetReadBuffer(4096); err != nil {
		t.Fatal(err)
	}
	file, err := sender.File()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	fd := int(file.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, err := unix.Write(fd, make([]byte, 65536))
		if err != nil && err != unix.EAGAIN {
			t.Fatal(err)
		}
		info, err := unix.GetsockoptTCPInfo(fd, unix.IPPROTO_TCP, unix.TCP_INFO)
		if err != nil {
			t.Fatal(err)
		}
		if info.Notsent_bytes > 32768 {
			return file, receiver
		}
	}
	t.Fatal("could not produce real TCP unsent backlog")
	return nil, nil
}

func TestTCPGuardBlocksBulkAllowsInteractiveAndResumes(t *testing.T) {
	tcp, receiver := congestedTCP(t)
	ctx, path := fixture(t, traffic.DefaultConfig())
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	client, err := openWithTCP(ctx, path, write, int(tcp.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if !client.TCPQueueGuarded() {
		t.Fatal("observer was not acknowledged")
	}
	bulk := make(chan error, 1)
	go func() { bulk <- client.WritePacket(ctx, "bulk", traffic.Bulk, []byte("bulk")) }()
	select {
	case err := <-bulk:
		t.Fatalf("bulk bypassed real TCP backlog: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	step, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if err := client.WritePacket(step, "ping", traffic.Interactive, []byte("ping")); err != nil {
		t.Fatal("interactive packet blocked by TCP guard", err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(read, got); err != nil || string(got) != "ping" {
		t.Fatal("incorrect first packet", string(got), err)
	}
	peer, peerRead := pipeClient(t, ctx, path)
	if err := peer.WritePacket(step, "other", traffic.Bulk, []byte("peer")); err != nil {
		t.Fatal("other destination blocked", err)
	}
	if _, err := io.ReadFull(peerRead, got); err != nil || string(got) != "peer" {
		t.Fatal("incorrect peer packet", err)
	}
	drained := make(chan error, 1)
	receiver.SetReadDeadline(time.Now().Add(5 * time.Second))
	go func() { _, err := io.Copy(io.Discard, receiver); drained <- err }()
	select {
	case err := <-bulk:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("bulk did not resume when TCP drained")
	}
	if _, err := io.ReadFull(read, got); err != nil || string(got) != "bulk" {
		t.Fatal("incorrect resumed packet", err)
	}
	// The broker must release its duplicate too; otherwise the peer never sees
	// EOF even after the application closes the original descriptor.
	client.Close()
	tcp.Close()
	if err := <-drained; err != nil {
		t.Fatal("broker retained TCP descriptor after disconnect", err)
	}
}

func TestTCPGuardCancellationReleasesQueuedWrite(t *testing.T) {
	tcp, receiver := congestedTCP(t)
	ctx, path := fixture(t, traffic.DefaultConfig())
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	client, err := openWithTCP(ctx, path, write, int(tcp.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	step, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	if err := client.WritePacket(step, "bulk", traffic.Bulk, []byte("never")); err == nil {
		t.Fatal("congested write escaped cancellation")
	}
	tcp.Close()
	receiver.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, receiver); err != nil {
		t.Fatal("canceled client retained socket", err)
	}
}

func TestQueueGuardCountsUndrainedPipe(t *testing.T) {
	tcp, receiver := congestedTCP(t)
	receiver.SetReadDeadline(time.Now().Add(5 * time.Second))
	go io.Copy(io.Discard, receiver)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if _, err := write.Write(bytes.Repeat([]byte("p"), 4096)); err != nil {
		t.Fatal(err)
	}
	queued, err := queuedOutput(int(write.Fd()), int(tcp.Fd()))
	if err != nil || queued < 4096 {
		t.Fatal("pipe backlog was omitted", queued, err)
	}
}

func TestInheritedSSHEnvironmentDoesNotAttachUnrelatedOutput(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "127.0.0.1 12345 127.0.0.1 22")
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if fd := discoverSSHSocket(write); fd >= 0 {
		unix.Close(fd)
		t.Fatal("attached without an SSH process owning this pipe")
	}
}
