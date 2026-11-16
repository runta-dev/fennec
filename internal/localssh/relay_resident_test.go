package localssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func TestResidentRelayReplenishesParkedStreamsAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	go func() {
		for {
			conn, err := local.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	parked := make(chan net.Conn, 128)
	done := make(chan error, 1)
	go func() {
		done <- serveResidentRelay(ctx, func(context.Context) (net.Conn, error) { a, b := net.Pipe(); parked <- b; return a, nil }, local.Addr().String())
	}()
	var conns []net.Conn
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()
	for i := range 24 {
		var conn net.Conn
		select {
		case conn = <-parked:
		case <-time.After(time.Second):
			t.Fatal("parked streams exhausted", i)
		}
		conns = append(conns, conn)
		conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write([]byte{'F'}); err != nil {
			t.Fatal(err)
		}
		data := []byte(fmt.Sprintf("reverse-%d", i))
		if _, err := conn.Write(data); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(data))
		if _, err := io.ReadFull(conn, got); err != nil || string(got) != string(data) {
			t.Fatal("reverse forwarding changed bytes", err)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("resident relay did not release active and parked streams")
	}
	for {
		select {
		case conn := <-parked:
			conn.Close()
		default:
			return
		}
	}
}
