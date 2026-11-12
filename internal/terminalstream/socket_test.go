package terminalstream

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSocketStreamRelaysBytesWithinCreditWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverInput, clientInput := io.Pipe()
	serverOutput, clientOutput := io.Pipe()
	defer clientInput.Close()
	defer serverOutput.Close()
	targets := make(chan string, 1)
	go ServeWithSockets(ctx, serverInput, clientOutput, nil, nil, nil, nil, func(ctx context.Context, target string) (net.Conn, error) {
		targets <- target
		local, remote := net.Pipe()
		go func() { io.Copy(remote, remote) }() // echo server
		return local, nil
	})
	transport := startTransport(clientInput, serverOutput, func() error { return nil }, func() error { return nil }, nil)
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if !transport.Supports(CapabilityCodexAppServer) {
		t.Fatal("socket capability not advertised")
	}
	session, err := transport.Launch(Frame{Type: "socket", ID: strings.Repeat("c", 32), Target: SocketCodexAppServer})
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := session.Next(ctx); err != nil || frame.Type != "receipt" {
		t.Fatal(frame, err)
	}
	if target := <-targets; target != SocketCodexAppServer {
		t.Fatal(target)
	}
	payload := bytes.Repeat([]byte("0123456789abcdef"), 3*Window/16)
	inputWindow := NewCreditWindow()
	go func() {
		for data := payload; len(data) > 0; {
			count := inputWindow.Take(min(len(data), Chunk))
			if count == 0 || session.Send(Frame{Type: "input", Data: data[:count]}) != nil {
				return
			}
			data = data[count:]
		}
	}()
	var received bytes.Buffer
	for received.Len() < len(payload) {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch frame.Type {
		case "data":
			received.Write(frame.Data)
			if err := session.Send(Frame{Type: "credit", Bytes: len(frame.Data)}); err != nil {
				t.Fatal(err)
			}
		case "input_credit":
			if !inputWindow.Add(frame.Bytes) {
				t.Fatal("invalid input credit")
			}
		default:
			t.Fatal("unexpected frame", frame.Type)
		}
	}
	if !bytes.Equal(received.Bytes(), payload) {
		t.Fatal("relayed bytes differ")
	}
	session.Send(Frame{Type: "close"})
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type == "exit" {
			break
		}
	}
}

func TestSocketStreamRejectsOtherTargets(t *testing.T) {
	for _, frame := range []Frame{
		{Type: "socket", ID: strings.Repeat("d", 32), Target: "/var/run/docker.sock"},
		{Type: "socket", ID: strings.Repeat("d", 32)},
	} {
		if frame.validateOpen() == nil {
			t.Fatal("socket target accepted", frame.Target)
		}
	}
}

func TestServerWithoutDialerDoesNotAdvertiseSockets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverInput, clientInput := io.Pipe()
	serverOutput, clientOutput := io.Pipe()
	defer clientInput.Close()
	defer serverOutput.Close()
	go ServeWithOperations(ctx, serverInput, clientOutput, nil, nil, nil, nil)
	transport := startTransport(clientInput, serverOutput, func() error { return nil }, func() error { return nil }, nil)
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if transport.Supports(CapabilityCodexAppServer) {
		t.Fatal("socket capability advertised without a dialer")
	}
}

type labeledConn struct{ net.Conn }

func (labeledConn) StreamLabel() string { return "native" }

func TestSocketReceiptReportsServedMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverInput, clientInput := io.Pipe()
	serverOutput, clientOutput := io.Pipe()
	defer clientInput.Close()
	defer serverOutput.Close()
	go ServeWithSockets(ctx, serverInput, clientOutput, nil, nil, nil, nil, func(context.Context, string) (net.Conn, error) {
		local, remote := net.Pipe()
		go io.Copy(io.Discard, remote)
		return labeledConn{local}, nil
	})
	transport := startTransport(clientInput, serverOutput, func() error { return nil }, func() error { return nil }, nil)
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	session, err := transport.Launch(Frame{Type: "socket", ID: strings.Repeat("e", 32), Target: SocketCodexAppServer})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if frame, err := session.Next(ctx); err != nil || frame.Type != "receipt" || frame.Message != "native" {
		t.Fatal(frame, err)
	}
}

// A socket stream that requests a large window receives far more than 64 KiB
// before granting any credit; streams that do not ask keep the PTY window.
func TestSocketStreamHonorsRequestedOutputWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverInput, clientInput := io.Pipe()
	serverOutput, clientOutput := io.Pipe()
	defer clientInput.Close()
	defer serverOutput.Close()
	payload := bytes.Repeat([]byte("w"), 1<<20)
	go ServeWithSockets(ctx, serverInput, clientOutput, nil, nil, nil, nil, func(context.Context, string) (net.Conn, error) {
		local, remote := net.Pipe()
		go func() { remote.Write(payload) }()
		return local, nil
	})
	transport := startTransport(clientInput, serverOutput, func() error { return nil }, func() error { return nil }, nil)
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	session, err := transport.Launch(Frame{Type: "socket", ID: strings.Repeat("f", 32), Target: SocketCodexAppServer, Bytes: SocketWindow})
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := session.Next(ctx); err != nil || frame.Type != "receipt" {
		t.Fatal(frame, err)
	}
	received := 0
	for received < len(payload) {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatal("stalled without credit after", received, err)
		}
		received += len(frame.Data)
	}
}
