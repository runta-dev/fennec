package bench

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"time"
)

// Relay forwards TCP connections from listen to target, so VMs that reach
// the host only on its bridge address can use a model gateway that listens
// on the host's loopback. The bridge address exists only while a VM runs, so
// a listen address that is not yet assigned is bound as soon as it appears,
// and bound again if its listener fails. Relay returns the listen address
// and runs until ctx ends; other bind errors are returned at once.
func Relay(ctx context.Context, listen, target string) (string, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", listen)
	if err != nil && !errors.Is(err, syscall.EADDRNOTAVAIL) {
		return "", err
	}
	addr := listen
	if ln != nil {
		addr = ln.Addr().String()
	}
	go func() {
		for ctx.Err() == nil {
			if ln == nil {
				ln, _ = lc.Listen(ctx, "tcp", addr)
				if ln == nil {
					select {
					case <-time.After(time.Second):
					case <-ctx.Done():
					}
					continue
				}
			}
			serveRelay(ctx, ln, target)
			_ = ln.Close()
			ln = nil
		}
	}()
	return addr, nil
}

// serveRelay accepts until the listener fails or ctx ends.
func serveRelay(ctx context.Context, ln net.Listener, target string) {
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
		case <-stop:
		}
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go pipe(ctx, c, target)
	}
}

func pipe(ctx context.Context, c net.Conn, target string) {
	defer c.Close()
	var d net.Dialer
	t, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return
	}
	defer t.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	half := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}
	go half(t, c)
	go half(c, t)
	wg.Wait()
}
