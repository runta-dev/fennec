package cli

import (
	"context"
	"fennec/internal/connectivity"
	"fennec/internal/launcher"
	"fennec/internal/launchprofile"
	"fennec/internal/terminalstream"
	"golang.org/x/crypto/ssh"
	"strconv"
	"sync"
	"time"
)

type terminalChannel struct {
	onListing       func(context.Context, []byte)
	ctx             context.Context
	network         *connectivity.Monitor
	shared          *launcher.SharedConnection
	preparedSSH     *ssh.Client
	connect         func(context.Context) (*ssh.Client, error)
	home, runtimeID string
	mu              sync.Mutex
	pending         chan struct{}
	transport       *terminalstream.Transport
	service         *fenSessionService
	err             error
	generation      uint64
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
				transport.Abort()
			}
		}()
		for {
			if channel.network != nil {
				if !channel.network.Online() {
					if transport := channel.peekAlive(); transport != nil {
						transport.Suspend()
					}
				}
				if channel.network.WaitOnline(channel.ctx) != nil {
					return
				}
			}
			var netChanged <-chan struct{}
			if channel.network != nil {
				netChanged = channel.network.Changes()
			}
			transport, _ := channel.get(channel.ctx)
			var disconnected <-chan struct{}
			if transport != nil {
				disconnected = transport.Done()
			}
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-channel.ctx.Done():
				timer.Stop()
				return
			case <-netChanged:
				timer.Stop()
				if transport != nil {
					probe, cancel := context.WithTimeout(channel.ctx, 1500*time.Millisecond)
					offline := channel.network != nil && !channel.network.Online()
					if offline {
						transport.Suspend()
					} else if !transport.Suspended() && transport.Probe(probe) != nil {
						launchprofile.Record("connection.retired_topology", time.Now(), 0)
						transport.Abort()
					}
					cancel()
				}
			case <-disconnected:
				timer.Stop()
			case <-timer.C:
			}
		}
	}()
	return done
}

func (channel *terminalChannel) peekAlive() *terminalstream.Transport {
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if channel.transport != nil && channel.transport.Alive() {
		return channel.transport
	}
	return nil
}

func (channel *terminalChannel) get(ctx context.Context) (*terminalstream.Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if channel.network != nil && !channel.network.Online() {
		channel.mu.Lock()
		transport := channel.transport
		channel.err = connectivity.ErrOffline
		channel.mu.Unlock()
		if transport != nil {
			transport.Suspend()
		}
		return nil, connectivity.ErrOffline
	}
	channel.mu.Lock()
	if channel.pending == nil && channel.transport != nil && channel.transport.Alive() && !channel.transport.Suspended() {
		transport := channel.transport
		channel.mu.Unlock()
		return transport, nil
	}
	if channel.pending == nil {
		previousFailed := channel.transport != nil
		previous := channel.transport
		channel.pending = make(chan struct{})
		pending := channel.pending
		go func() {
			defer launchprofile.Begin("connection.recover")()
			operation, cancel := context.WithTimeout(channel.ctx, 35*time.Second)
			defer cancel()
			if channel.network != nil {
				changed := channel.network.Changes()
				go func() {
					select {
					case <-changed:
						cancel()
					case <-operation.Done():
					}
				}()
			}
			var transport *terminalstream.Transport
			var err error
			preserved := false
			if previous != nil && previous.Suspended() && previous.Alive() {
				reused, replacement, recoveryErr := raceConnectionRecovery(operation, func(probe context.Context) error {
					defer launchprofile.Begin("connection.resume_existing")()
					return previous.Resume(probe)
				}, channel.connect, func(client *ssh.Client) { client.Close() })
				if reused {
					transport = previous
					launchprofile.Record("connection.reused_after_outage", time.Now(), 0)
				} else if operation.Err() != nil && channel.ctx.Err() == nil && previous.Alive() {
					// Wi-Fi/VPN changes arrive as a burst of routing events. A
					// canceled attempt says nothing about the retained TCP socket.
					// Keep it for the next route snapshot instead of destroying it.
					previous.Suspend()
					transport, err, preserved = previous, operation.Err(), true
					if replacement != nil {
						replacement.Close()
					}
					launchprofile.Record("connection.preserved_topology_change", time.Now(), 0)
				} else {
					previous.Abort()
					channel.preparedSSH, err = replacement, recoveryErr
				}
				previousFailed = false
			}
			if transport == nil && previousFailed && channel.preparedSSH != nil {
				channel.preparedSSH.Close()
				channel.preparedSSH = nil
			}
			if transport == nil && err == nil && channel.preparedSSH == nil && channel.connect != nil {
				channel.preparedSSH, err = channel.connect(operation)
			}
			if transport == nil && err == nil && channel.preparedSSH != nil {
				transport, err = terminalstream.StartSSHContext(operation, channel.preparedSSH, `"$HOME/.local/fennec/bin/fennec-terminal" serve --protocol=`+strconv.Itoa(terminalstream.ProtocolVersion))
				if err != nil {
					channel.preparedSSH.Close()
					channel.preparedSSH = nil
				}
			}
			if transport != nil {
				if err != nil && transport != nil && !preserved {
					transport.Close()
					transport = nil
				}
			}
			channel.mu.Lock()
			channel.transport = transport
			if err == nil && transport != nil {
				channel.generation++
			}
			channel.err = err
			channel.pending = nil
			close(pending)
			channel.mu.Unlock()
			if err == nil && transport != nil {
				// Publish first, then signal readiness. Otherwise a fast local
				// client can consume the wake before the new transport is visible.
				// Authorized requests may still be queued before the ready frame.
				go func() {
					readyCtx, cancel := context.WithTimeout(channel.ctx, 35*time.Second)
					defer cancel()
					if transport.Ready(readyCtx) != nil {
						transport.Abort()
						return
					}
					channel.mu.Lock()
					current := channel.transport == transport && channel.ctx.Err() == nil
					channel.mu.Unlock()
					if current {
						notifyConnectionReady(channel.home)
					}
				}()
			}
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
	if channel.err != nil {
		return nil, channel.err
	}
	return channel.transport, channel.err
}
