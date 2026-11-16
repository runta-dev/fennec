package localssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync/atomic"
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

func TestResidentRelayReplacesFailedIdleBridgeWithoutDroppingActiveCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			io.Copy(conn, conn)
		}
	}()
	parked := make(chan net.Conn, 32)
	var failNext atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- serveResidentRelay(ctx, func(context.Context) (net.Conn, error) {
			if failNext.Swap(false) {
				return nil, io.ErrUnexpectedEOF
			}
			a, b := net.Pipe()
			parked <- b
			return a, nil
		}, listener.Addr().String())
	}()
	var all []net.Conn
	defer func() {
		cancel()
		for _, conn := range all {
			conn.Close()
		}
	}()
	take := func() net.Conn {
		t.Helper()
		select {
		case conn := <-parked:
			all = append(all, conn)
			return conn
		case err := <-done:
			t.Fatal("one idle bridge failure retired the entire relay", err)
		case <-time.After(3 * time.Second):
			t.Fatal("replacement bridge was not opened")
		}
		return nil
	}
	for range parkedBridges {
		take()
	}
	active := all[0]
	active.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := active.Write([]byte{'F'}); err != nil {
		t.Fatal(err)
	}
	take() // Replacement for the claimed connection.
	failNext.Store(true)
	all[1].Close() // An unrelated parked bridge disappears.
	take()         // The replacement must also survive one failed open.
	if _, err := active.Write([]byte("still-running")); err != nil {
		t.Fatal("idle bridge failure closed an active command", err)
	}
	got := make([]byte, len("still-running"))
	if _, err := io.ReadFull(active, got); err != nil || string(got) != "still-running" {
		t.Fatal("active connection lost its output", string(got), err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay did not stop on parent cancellation")
	}
}
