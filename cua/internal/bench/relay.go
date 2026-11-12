package bench

import (
	"context"
	"io"
	"net"
	"sync"
)

// Relay forwards TCP connections from listen to target, so VMs that reach
// the host only on its bridge address can use a model gateway that listens
// on the host's loopback. It returns the bound address and runs until ctx
// ends.
func Relay(ctx context.Context, listen, target string) (string, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", listen)
	if err != nil {
		return "", err
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go pipe(ctx, c, target)
		}
	}()
	return ln.Addr().String(), nil
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
