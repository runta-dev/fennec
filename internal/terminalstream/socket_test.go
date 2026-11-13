package terminalstream

import (
	"bytes"
	"context"
	"github.com/klauspost/compress/zstd"
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

// Compressed socket output decodes to the exact bytes, arrives message by
// message, and repeated replies cost far less than their size.
func TestSocketStreamCompressesOutputWhenRequested(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	serverInput, clientInput := io.Pipe()
	serverOutput, clientOutput := io.Pipe()
	defer clientInput.Close()
	defer serverOutput.Close()
	reply := []byte(strings.Repeat(`{"name":"tool","inputSchema":{"type":"object"}},`, 20000))
	go ServeWithSockets(ctx, serverInput, clientOutput, nil, nil, nil, nil, func(context.Context, string) (net.Conn, error) {
		local, remote := net.Pipe()
		go func() {
			remote.Write(reply)
			remote.Write(reply)
			remote.Close()
		}()
		return local, nil
	})
	transport := startTransport(clientInput, serverOutput, func() error { return nil }, func() error { return nil }, nil)
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	session, err := transport.Launch(Frame{Type: "socket", ID: strings.Repeat("a", 32), Target: SocketCodexAppServer, Term: SocketCompressionZstd})
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := session.Next(ctx); err != nil || frame.Type != "receipt" || frame.Term != SocketCompressionZstd {
		t.Fatal(frame, err)
	}
	reader, writer := io.Pipe()
	decoded := make(chan []byte, 1)
	go func() {
		decoder, _ := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1))
		data, _ := io.ReadAll(decoder)
		decoded <- data
	}()
	wire := 0
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type == "exit" {
			break
		}
		if frame.Type == "data" {
			wire += len(frame.Data)
			writer.Write(frame.Data)
			session.Send(Frame{Type: "credit", Bytes: len(frame.Data)})
		}
	}
	writer.Close()
	if got := <-decoded; !bytes.Equal(got, append(append([]byte(nil), reply...), reply...)) {
		t.Fatal("decoded output differs", len(got))
	}
	if wire*20 > 2*len(reply) {
		t.Fatal("output not compressed:", wire, "bytes for", 2*len(reply))
	}
}

// The ChatGPT bridge sends the WebSocket upgrade together with the open; input
// that arrives while the socket is still being dialed must reach it in order.
func TestSocketInputSentBeforeReceiptIsDelivered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverInput, clientInput := io.Pipe()
	serverOutput, clientOutput := io.Pipe()
	defer clientInput.Close()
	defer serverOutput.Close()
	dialing := make(chan struct{})
	release := make(chan struct{})
	go ServeWithSockets(ctx, serverInput, clientOutput, nil, nil, nil, nil, func(ctx context.Context, target string) (net.Conn, error) {
		close(dialing)
		<-release // a slow dial: input is already on its way
		local, remote := net.Pipe()
		go func() { io.Copy(remote, remote) }()
		return local, nil
	})
	transport := startTransport(clientInput, serverOutput, func() error { return nil }, func() error { return nil }, nil)
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	session, err := transport.Launch(Frame{Type: "socket", ID: strings.Repeat("e", 32), Target: SocketCodexAppServer})
	if err != nil {
		t.Fatal(err)
	}
	request := []byte("GET /rpc HTTP/1.1\r\n\r\n")
	if err := session.Send(Frame{Type: "input", Data: request[:9]}); err != nil {
		t.Fatal(err)
	}
	<-dialing
	if err := session.Send(Frame{Type: "input", Data: request[9:]}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // both frames reach the server before the dial finishes
	close(release)
	if frame, err := session.Next(ctx); err != nil || frame.Type != "receipt" {
		t.Fatal(frame, err)
	}
	var received []byte
	for len(received) < len(request) {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type == "data" {
			received = append(received, frame.Data...)
			session.Send(Frame{Type: "credit", Bytes: len(frame.Data)})
		}
	}
	if !bytes.Equal(received, request) {
		t.Fatalf("%q", received)
	}
}
