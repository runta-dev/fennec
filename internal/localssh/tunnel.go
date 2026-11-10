package localssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ReverseTunnel accepts connections on a remote loopback listener and forwards
// bytes to the local SSH server. Authentication and approvals remain end-to-end
// in that server. Only a literal local loopback address is accepted.
func ReverseTunnel(ctx context.Context, client *ssh.Client, localAddress string, ready func(string)) error {
	host, _, err := net.SplitHostPort(localAddress)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("local SSH endpoint must be a literal loopback address")
	}
	listener, err := client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("remote loopback forwarding: %w", err)
	}
	defer listener.Close()
	if ready != nil {
		ready(listener.Addr().String())
	}
	return serveTunnel(ctx, listener, localAddress)
}

func serveTunnel(ctx context.Context, listener net.Listener, localAddress string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	defer workers.Wait()
	go func() { <-ctx.Done(); listener.Close() }()
	slots := make(chan struct{}, 16)
	for {
		remote, err := listener.Accept()
		if err != nil {
			wasCancelled := ctx.Err() != nil
			cancel()
			if wasCancelled {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			remote.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			defer remote.Close()
			local, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", localAddress)
			if err != nil {
				return
			}
			defer local.Close()
			finished := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					local.Close()
					remote.Close()
				case <-finished:
				}
			}()
			defer close(finished)
			var copies sync.WaitGroup
			copies.Add(2)
			copyHalf := func(dst, src net.Conn) {
				defer copies.Done()
				io.Copy(dst, src)
				if half, ok := dst.(interface{ CloseWrite() error }); ok {
					half.CloseWrite()
				} else {
					dst.Close()
				}
			}
			go copyHalf(local, remote)
			go copyHalf(remote, local)
			copies.Wait()
		}()
	}
}
