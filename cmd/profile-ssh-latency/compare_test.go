package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fennec/internal/commandstream"
	"fennec/internal/terminalstream"
	"io"
	"net"
	"testing"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

func TestComparisonLoadDrainsBothVariants(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &gliderssh.Server{Handler: func(s gliderssh.Session) {
		if s.RawCommand() != "cat" {
			s.Exit(127)
			return
		}
		_, err := io.Copy(s, s)
		if err != nil {
			s.Exit(1)
		} else {
			s.Exit(0)
		}
	}}
	server.AddHostKey(signer)
	defer server.Close()
	go server.Serve(listener)
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	done := make(chan error, 1)
	go func() {
		defer output.Close()
		done <- terminalstream.ServeWithSockets(ctx, input, output, nil, nil, nil, nil, func(ctx context.Context, _ string) (net.Conn, error) { return commandstream.Dial(ctx), nil })
	}()
	transport := terminalstream.StartPipes(send, receive)
	defer func() { transport.Close(); cancel(); <-done }()
	for _, variant := range []string{"baseline", "candidate"} {
		load, err := beginComparisonLoad(ctx, client, transport, variant)
		if err != nil {
			t.Fatal(variant, err)
		}
		block, err := load.finish(ctx)
		if err != nil || block.Sent < 64<<10 || block.Sent != block.Received {
			t.Fatalf("%s transfer: %+v %v", variant, block, err)
		}
	}
}
