package terminalstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/cloudops"
	"fennec/internal/traffic"
	"fennec/internal/trafficbroker"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResidentSharedBrokerPreemptsLargeReply(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "fen-packet-broker-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listener, err := trafficbroker.Listen(filepath.Join(dir, "out.sock"))
	if err != nil {
		t.Fatal(err)
	}
	service := make(chan error, 1)
	go func() { service <- trafficbroker.Serve(ctx, listener, traffic.DefaultConfig()) }()
	defer func() {
		cancel()
		if err := <-service; err != nil {
			t.Error(err)
		}
	}()
	receive, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer receive.Close()
	defer output.Close()
	broker, err := trafficbroker.Open(ctx, filepath.Join(dir, "out.sock"), output)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	input, send := io.Pipe()
	defer input.Close()
	defer send.Close()
	started := make(chan struct{})
	result := json.RawMessage(`{"screen":"` + strings.Repeat("x", 96<<10) + `"}`)
	server := make(chan error, 1)
	go func() {
		server <- ServeWithOperations(ctx, input, broker, nil, nil, nil, func(context.Context, cloudops.Operation) (json.RawMessage, error) {
			close(started)
			return result, nil
		})
	}()
	transport := StartPipes(send, receive)
	defer transport.Close()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	operation := make(chan error, 1)
	go func() {
		got, err := transport.Operate(ctx, cloudops.Operation{Kind: "list"})
		if err == nil && !bytes.Equal(got, result) {
			err = io.ErrUnexpectedEOF
		}
		operation <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Sampling several fresh IDs while the reply transfers exercises the
	// production packet-writer delegation, including a waiting bulk token.
	for range 10 {
		if rtt, err := transport.Ping(ctx); err != nil || rtt > 100*time.Millisecond {
			t.Fatalf("shared-writer ping: %s, %v", rtt, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := <-operation; err != nil {
		t.Fatal(err)
	}
	transport.Close()
	cancel()
	select {
	case <-server:
	case <-time.After(time.Second):
		t.Fatal("resident failed to close")
	}
}
