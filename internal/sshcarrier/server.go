// Package sshcarrier keeps an authenticated SSH transport alive across local
// daemon replacements. It forwards channels, never interprets or retries commands.
package sshcarrier

import (
	"bytes"
	"context"
	"fennec/internal/launchprofile"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	leaseRequest byte = 1
	stopRequest  byte = 2
	defaultIdle       = 30 * time.Second
)

type server struct {
	ctx           context.Context
	cancel        context.CancelFunc
	dial          func(context.Context) (*ssh.Client, error)
	signer        ssh.Signer
	listener      net.Listener
	idle          time.Duration
	mu            sync.Mutex
	upstream      *ssh.Client
	sessions      *sessionPool
	pending       chan struct{}
	err           error
	leases        map[net.Conn]*ssh.Client
	lastRelease   time.Time
	health        healthPolicy
	healthStarted bool
	upstreamEnded chan struct{}
	activity      *atomic.Int64
}

func serve(ctx context.Context, listener net.Listener, signer ssh.Signer, dial func(context.Context) (*ssh.Client, error), idle time.Duration, policies ...healthPolicy) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &server{ctx: ctx, cancel: cancel, listener: listener, signer: signer, dial: dial, idle: idle, leases: map[net.Conn]*ssh.Client{}, lastRelease: time.Now(), health: defaultHealthPolicy()}
	if len(policies) == 1 {
		s.health = policies[0]
	}
	stop := context.AfterFunc(ctx, func() {
		listener.Close()
		s.mu.Lock()
		defer s.mu.Unlock()
		for conn := range s.leases {
			conn.Close()
		}
		if s.upstream != nil {
			s.upstream.Close()
		}
		if s.sessions != nil {
			s.sessions.close()
		}
	})
	defer stop()
	go s.expire()
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			cancel()
			return err
		}
		s.mu.Lock()
		if ctx.Err() != nil {
			s.mu.Unlock()
			conn.Close()
			return nil
		}
		s.leases[conn] = nil
		s.mu.Unlock()
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer conn.Close()
			defer func() {
				s.mu.Lock()
				delete(s.leases, conn)
				s.lastRelease = time.Now()
				s.mu.Unlock()
			}()
			s.lease(conn)
		}()
	}
}

func (s *server) expire() {
	ticker := time.NewTicker(min(s.idle, time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			expired := len(s.leases) == 0 && time.Since(s.lastRelease) >= s.idle
			if expired {
				s.cancel()
			}
			s.mu.Unlock()
			if expired {
				return
			}
		}
	}
}

// Local leases have independent channel lifetimes. Closing one lease must not
// close the upstream connection or another daemon's in-flight channel.
func (s *server) lease(raw net.Conn) {
	raw.SetDeadline(time.Now().Add(10 * time.Second))
	var operation [1]byte
	if _, err := io.ReadFull(raw, operation[:]); err != nil {
		return
	}
	if operation[0] == stopRequest {
		s.cancel()
		return
	}
	if operation[0] != leaseRequest {
		return
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != localLeaseUser || !bytes.Equal(key.Marshal(), s.signer.PublicKey().Marshal()) {
			return nil, fmt.Errorf("transport identity mismatch")
		}
		return nil, nil
	}}
	config.AddHostKey(s.signer)
	local, channels, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		return
	}
	raw.SetDeadline(time.Time{})
	defer local.Close()
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	go func() { local.Wait(); cancel() }()
	upstream, err := s.connection(ctx)
	if err != nil {
		return
	}
	s.mu.Lock()
	if s.upstream != upstream {
		s.mu.Unlock()
		return
	}
	s.leases[raw] = upstream
	s.mu.Unlock()
	go func() {
		for request := range requests {
			ok, payload, err := upstream.SendRequest(request.Type, request.WantReply, request.Payload)
			if err != nil {
				local.Close()
				return
			}
			if request.WantReply {
				request.Reply(ok, payload)
			}
		}
	}()
	var workers sync.WaitGroup
	defer workers.Wait()
	for incoming := range channels {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if incoming.ChannelType() != "session" && incoming.ChannelType() != "direct-tcpip" {
				incoming.Reject(ssh.UnknownChannelType, "unsupported channel")
				return
			}
			// Opening a command channel is gated by the daemon's fresh ownership
			// validation. Speculative authentication alone cannot start a process.
			s.mu.Lock()
			if s.upstream != upstream {
				s.mu.Unlock()
				incoming.Reject(ssh.ConnectionFailed, "upstream connection retired")
				return
			}
			if !s.healthStarted {
				s.healthStarted = true
				go s.watchHealth(upstream, s.upstreamEnded, s.activity)
			}
			pool := s.sessions
			activity := s.activity
			s.mu.Unlock()
			var remote ssh.Channel
			var remoteRequests <-chan *ssh.Request
			var err error
			if incoming.ChannelType() == "session" && len(incoming.ExtraData()) == 0 && pool != nil && pool.client == upstream {
				remote, remoteRequests, err = pool.take()
			} else {
				remote, remoteRequests, err = upstream.OpenChannel(incoming.ChannelType(), incoming.ExtraData())
			}
			if err != nil {
				incoming.Reject(ssh.ConnectionFailed, "upstream channel unavailable")
				return
			}
			defer remote.Close()
			localChannel, localRequests, err := incoming.Accept()
			if err != nil {
				return
			}
			defer localChannel.Close()
			stop := context.AfterFunc(ctx, func() { remote.Close(); localChannel.Close() })
			defer stop()
			bridge(localChannel, localRequests, remote, remoteRequests, activity)
		}()
	}
}

func (s *server) connection(ctx context.Context) (*ssh.Client, error) {
	s.mu.Lock()
	if s.upstream != nil {
		client := s.upstream
		s.mu.Unlock()
		launchprofile.Record("ssh.carrier_reused", time.Now(), 0)
		return client, nil
	}
	if s.pending == nil {
		done := make(chan struct{})
		s.pending = done
		go func() {
			finish := launchprofile.Begin("ssh.carrier_connect")
			attempt, cancel := context.WithTimeout(s.ctx, 20*time.Second)
			defer cancel()
			client, err := s.dial(attempt)
			finish()
			s.mu.Lock()
			if s.ctx.Err() != nil && client != nil {
				client.Close()
				client, err = nil, s.ctx.Err()
			}
			s.upstream, s.err, s.pending = client, err, nil
			if client != nil {
				s.sessions = newSessionPool(client)
				s.healthStarted = false
				s.upstreamEnded = make(chan struct{})
				s.activity = &atomic.Int64{}
			}
			ended := s.upstreamEnded
			close(done)
			s.mu.Unlock()
			if client != nil {
				client.Wait()
				s.retire(client)
				// The channel is created once for this connection generation.
				close(ended)
			}
		}()
	}
	done := s.pending
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upstream == nil && s.err == nil {
		return nil, net.ErrClosed
	}
	return s.upstream, s.err
}

// Retire only this upstream and leases bound to it. A late failure must not
// close a replacement connection or a local handshake still awaiting binding.
func (s *server) retire(client *ssh.Client) {
	s.mu.Lock()
	if s.upstream != client {
		s.mu.Unlock()
		return
	}
	s.upstream = nil
	pool := s.sessions
	s.sessions = nil
	var leases []net.Conn
	for lease, upstream := range s.leases {
		if upstream == client {
			leases = append(leases, lease)
		}
	}
	s.mu.Unlock()
	// Close the underlying socket before channel cleanup, which can otherwise
	// block while trying to write channel-close into the failed TCP connection.
	client.Close()
	for _, lease := range leases {
		lease.Close()
	}
	if pool != nil {
		pool.close()
	}
}

type runtimeReader struct {
	io.Reader
	activity *atomic.Int64
}

func (r runtimeReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.activity.Store(time.Now().UnixNano())
	}
	return n, err
}

func bridge(a ssh.Channel, ar <-chan *ssh.Request, b ssh.Channel, br <-chan *ssh.Request, activity *atomic.Int64) {
	var requestReply sync.Mutex
	var workers sync.WaitGroup
	run := func(f func()) { workers.Add(1); go func() { defer workers.Done(); f() }() }
	run(func() {
		for request := range ar {
			requestReply.Lock()
			ok, err := b.SendRequest(request.Type, request.WantReply, request.Payload)
			if request.WantReply {
				request.Reply(err == nil && ok, nil)
			}
			requestReply.Unlock()
			if err != nil {
				break
			}
		}
		b.Close()
	})
	run(func() { io.Copy(b.Stderr(), a.Stderr()) })
	run(func() { io.Copy(b, a); b.CloseWrite() })
	var output sync.WaitGroup
	output.Add(2)
	go func() { defer output.Done(); io.Copy(a, runtimeReader{b, activity}) }()
	go func() { defer output.Done(); io.Copy(a.Stderr(), runtimeReader{b.Stderr(), activity}) }()
	// EOF may precede exit-status. Drain remote requests and both output
	// streams before closing, and do not overtake an exec acknowledgement.
	for request := range br {
		ok, err := a.SendRequest(request.Type, request.WantReply, request.Payload)
		if request.WantReply {
			request.Reply(err == nil && ok, nil)
		}
	}
	output.Wait()
	requestReply.Lock()
	a.CloseWrite()
	a.Close()
	requestReply.Unlock()
	b.Close()
	workers.Wait()
}
