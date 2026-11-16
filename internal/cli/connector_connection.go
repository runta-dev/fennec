package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/localssh"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/terminalstream"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

func (s *daemonServices) dialResidentRelay(ctx context.Context, epoch uint64, runtimeID, device string, port int) (net.Conn, error) {
	s.mu.Lock()
	valid := s.epoch == epoch && s.ctx != nil && s.ctx.Err() == nil && s.config.RuntimeID == runtimeID
	channel := s.channel
	s.mu.Unlock()
	if !valid || channel == nil {
		return nil, fmt.Errorf("resident connector unavailable")
	}
	transport, err := channel.get(ctx)
	if err != nil {
		return nil, err
	}
	if err = transport.Ready(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	valid = s.epoch == epoch && s.ctx != nil && s.ctx.Err() == nil && s.config.RuntimeID == runtimeID
	s.mu.Unlock()
	if !valid {
		return nil, fmt.Errorf("connector account or runtime changed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var conn net.Conn
	if transport.Supports(terminalstream.CapabilityDeviceRelay) {
		conn, err = transport.DialSocket(ctx, terminalstream.SocketDeviceRelay, device)
	} else {
		// Compatibility only for an older remote binary. Once upgraded, all
		// reverse traffic uses the resident's directional budget and credits.
		client, e := s.linkSSH(ctx, epoch, runtimeID)
		if e != nil {
			return nil, e
		}
		conn, err = client.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	valid = s.epoch == epoch && s.ctx != nil && s.ctx.Err() == nil && s.config.RuntimeID == runtimeID
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		conn.Close()
		return nil, err
	}
	if !valid {
		conn.Close()
		return nil, fmt.Errorf("connector account or runtime changed")
	}
	return conn, nil
}

func (s *daemonServices) bootstrapLink(ctx context.Context, epoch uint64, runtimeID string, request localssh.LinkBootstrap) (localssh.LinkBootstrapResult, error) {
	// Wait locally for the shared resident published by preparation. This does
	// not create another SSH connection or issue speculative remote commands.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		valid := s.epoch == epoch && s.ctx != nil && s.ctx.Err() == nil && s.config.RuntimeID == runtimeID
		channel := s.channel
		s.mu.Unlock()
		if !valid {
			return localssh.LinkBootstrapResult{}, fmt.Errorf("connector account or runtime changed")
		}
		if channel != nil {
			transport, err := channel.get(ctx)
			if err != nil {
				return localssh.LinkBootstrapResult{}, err
			}
			if err = transport.Ready(ctx); err != nil {
				return localssh.LinkBootstrapResult{}, err
			}
			if !transport.Supports("connector_prepare") {
				return localssh.LinkBootstrapResult{NeedsSetup: true}, nil
			}
			data, err := transport.Operate(ctx, cloudruntime.LocalOperation{Kind: "connector_prepare", Connector: &request})
			if err != nil {
				return localssh.LinkBootstrapResult{}, err
			}
			var result localssh.LinkBootstrapResult
			if json.Unmarshal(data, &result) != nil {
				return result, fmt.Errorf("invalid connector result")
			}
			return result, nil
		}
		select {
		case <-ctx.Done():
			return localssh.LinkBootstrapResult{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// The connector borrows the same authenticated connection as resident sessions.
// A reconnect waits on their one pending result instead of dialing separately.
func (s *daemonServices) linkSSH(ctx context.Context, epoch uint64, runtimeID string) (*ssh.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.epoch != epoch || s.ctx == nil || s.ctx.Err() != nil || s.config.RuntimeID != runtimeID {
		s.mu.Unlock()
		return nil, fmt.Errorf("connector account or runtime changed")
	}
	channel, prepared, preparedID := s.channel, s.config.PreparedSSH, s.config.PreparedRuntimeID
	s.mu.Unlock()
	var client *ssh.Client
	var err error
	if channel != nil {
		client, err = channel.borrowSSH(ctx)
	} else if prepared != nil && preparedID == runtimeID {
		// Preparation already verified ownership and host keys, but the resident
		// protocol may still be starting. Pairing can overlap that startup.
		client = prepared
	} else {
		return nil, fmt.Errorf("shared cloud connection is preparing")
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	valid := s.epoch == epoch && s.ctx != nil && s.ctx.Err() == nil && s.config.RuntimeID == runtimeID
	s.mu.Unlock()
	if !valid {
		return nil, fmt.Errorf("connector account or runtime changed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return client, nil
}

func (channel *terminalChannel) borrowSSH(ctx context.Context) (*ssh.Client, error) {
	for {
		transport, err := channel.get(ctx)
		if err != nil {
			return nil, err
		}
		channel.mu.Lock()
		if channel.pending == nil && channel.transport == transport && transport != nil && transport.Alive() && !transport.Suspended() {
			client := channel.preparedSSH
			channel.mu.Unlock()
			if client == nil {
				return nil, fmt.Errorf("shared SSH connection unavailable")
			}
			return client, nil
		}
		channel.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}
