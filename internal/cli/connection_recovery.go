package cli

import (
	"context"
	"time"

	"golang.org/x/crypto/ssh"
)

// A retained TCP connection can recover in one RTT or be unusable after a NAT
// change. Race its probe against one freshly authorized handshake, never a
// second session launch. The losing connection cannot execute any commands.
func raceConnectionRecovery(ctx context.Context, resume func(context.Context) error, connect func(context.Context) (*ssh.Client, error), discard func(*ssh.Client)) (bool, *ssh.Client, error) {
	probeCtx, cancelProbe := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancelProbe()
	probe := make(chan error, 1)
	go func() { probe <- resume(probeCtx) }()
	if connect == nil {
		err := <-probe
		return err == nil, nil, err
	}
	connectCtx, cancelConnect := context.WithCancel(ctx)
	defer cancelConnect()
	type result struct {
		client *ssh.Client
		err    error
	}
	fresh := make(chan result, 1)
	go func() { client, err := connect(connectCtx); fresh <- result{client, err} }()
	select {
	case err := <-probe:
		if err == nil {
			cancelConnect()
			go func() {
				if value := <-fresh; value.client != nil {
					discard(value.client)
				}
			}()
			return true, nil, nil
		}
		value := <-fresh
		if value.err != nil && value.client != nil {
			discard(value.client)
			value.client = nil
		}
		return false, value.client, value.err
	case value := <-fresh:
		if value.err == nil {
			return false, value.client, nil
		}
		if value.client != nil {
			discard(value.client)
		}
		if err := <-probe; err == nil {
			return true, nil, nil
		}
		return false, nil, value.err
	}
}
