package localssh

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestTunnelTrafficAndShutdown(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		c, e := target.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		io.Copy(c, c)
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveTunnel(ctx, listener, target.Addr().String()) }()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 5)
	if _, err = io.ReadFull(conn, data); err != nil || string(data) != "hello" {
		t.Fatalf("%q %v", data, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tunnel did not close active connections")
	}
}
func TestRejectPublicLocalTarget(t *testing.T) {
	for _, address := range []string{"0.0.0.0:22", "example.com:22", "10.0.0.1:22"} {
		if ReverseTunnel(context.Background(), nil, address, nil) == nil {
			t.Fatalf("accepted %s", address)
		}
	}
}
