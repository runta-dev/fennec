package localssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

// DialExistingRelay accepts a paired device, never a caller-provided port or
// filesystem path. Validate the recorded process before reaching its loopback
// bridge. The inner SSH layer still verifies the Mac key and authorizes commands.
func DialExistingRelay(ctx context.Context, home, device string) (net.Conn, error) {
	if !deviceIDPattern.MatchString(device) {
		return nil, fmt.Errorf("invalid relay device")
	}
	state, err := inspectExistingRelay(ctx, home, device, os.ReadFile)
	if err != nil {
		return nil, err
	}
	if !state.Ready {
		return nil, fmt.Errorf("device relay is not ready")
	}
	return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(state.BridgePort)))
}

// serveResidentRelay preserves parked-bridge capacity, cancellation and local
// SSH authentication while carrying bytes through the prioritized resident
// protocol. A stopped link closes only its streams, never the shared transport.
func serveResidentRelay(ctx context.Context, dial func(context.Context) (net.Conn, error), local string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	active := make(chan struct{}, maxRelayConnections)
	forward := func(remote net.Conn) {
		defer workers.Done()
		defer func() { <-active }()
		defer remote.Close()
		target, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", local)
		if err != nil {
			return
		}
		defer target.Close()
		stop := context.AfterFunc(ctx, func() { target.Close(); remote.Close() })
		defer stop()
		done := make(chan struct{})
		go func() { io.Copy(remote, target); remote.Close(); close(done) }()
		io.Copy(target, remote)
		target.Close()
		<-done
	}
	for range parkedBridges {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				opening, stopOpening := context.WithTimeout(ctx, 10*time.Second)
				remote, err := dial(opening)
				stopOpening()
				if err != nil {
					cancel()
					return
				}
				stop := context.AfterFunc(ctx, func() { remote.Close() })
				var marker [1]byte
				_, err = io.ReadFull(remote, marker[:])
				stop()
				if err != nil {
					remote.Close()
					cancel()
					return
				}
				if marker[0] != 'F' {
					remote.Close()
					continue
				}
				select {
				case active <- struct{}{}:
					workers.Add(1)
					go forward(remote)
				default:
					remote.Close()
				}
			}
		}()
	}
	<-ctx.Done()
	workers.Wait()
	return fmt.Errorf("device relay disconnected")
}
