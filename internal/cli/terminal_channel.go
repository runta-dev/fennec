package cli

import (
	"context"
	"fennec/internal/launcher"
	"fennec/internal/terminalstream"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type terminalChannel struct {
	ctx             context.Context
	shared          *launcher.SharedConnection
	home, runtimeID string
	mu              sync.Mutex
	pending         chan struct{}
	transport       *terminalstream.Transport
	service         *fenSessionService
	err             error
}

func (channel *terminalChannel) start() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			channel.mu.Lock()
			pending := channel.pending
			channel.mu.Unlock()
			if pending != nil {
				<-pending
			}
			channel.mu.Lock()
			transport := channel.transport
			channel.mu.Unlock()
			if transport != nil {
				transport.Close()
			}
		}()
		for {
			channel.get(channel.ctx)
			select {
			case <-channel.ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	return done
}

func (channel *terminalChannel) get(ctx context.Context) (*terminalstream.Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	channel.mu.Lock()
	if channel.transport != nil && channel.transport.Alive() {
		transport := channel.transport
		channel.mu.Unlock()
		return transport, nil
	}
	if channel.pending == nil {
		channel.pending = make(chan struct{})
		pending := channel.pending
		go func() {
			operation, cancel := context.WithTimeout(channel.ctx, 35*time.Second)
			defer cancel()
			control, err := channel.shared.Get(operation)
			var transport *terminalstream.Transport
			if err == nil {
				directory := filepath.Join(channel.home, ".config/fennec/cloud")
				command := exec.CommandContext(channel.ctx, "/usr/bin/ssh", "-T", "-S", control, "-o", "ControlMaster=no", "-o", "ProxyCommand=false", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "UserKnownHostsFile="+filepath.Join(directory, "known_hosts"), "-i", filepath.Join(directory, "runtime_key"), channel.runtimeID+"@ssh.runta.com", `"$HOME/.local/fennec/bin/fennec-terminal" serve`)
				transport, err = terminalstream.Start(command)
				if err == nil {
					err = transport.Ready(operation)
				}
				if err != nil && transport != nil {
					transport.Close()
					transport = nil
				}
			}
			channel.mu.Lock()
			channel.transport = transport
			channel.err = err
			channel.pending = nil
			close(pending)
			channel.mu.Unlock()
		}()
	}
	pending := channel.pending
	channel.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-pending:
	}
	channel.mu.Lock()
	defer channel.mu.Unlock()
	return channel.transport, channel.err
}
