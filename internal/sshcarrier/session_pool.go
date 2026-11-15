package sshcarrier

import (
	"sync"

	"golang.org/x/crypto/ssh"
)

type idleSession struct {
	channel  ssh.Channel
	requests <-chan *ssh.Request
}

// Only empty SSH channels are retained, never commands or agent processes.
// Filling starts after the daemon's first authorized session request. Subsequent
// daemon replacements can send exec without another WAN channel-open round trip.
type sessionPool struct {
	client  *ssh.Client
	mu      sync.Mutex
	filling bool
	closed  bool
	idle    chan idleSession
}

func newSessionPool(client *ssh.Client) *sessionPool {
	return &sessionPool{client: client, idle: make(chan idleSession, 2)}
}

func (p *sessionPool) take() (ssh.Channel, <-chan *ssh.Request, error) {
	select {
	case session := <-p.idle:
		go p.fill()
		return session.channel, session.requests, nil
	default:
		channel, requests, err := p.client.OpenChannel("session", nil)
		if err == nil {
			go p.fill()
		}
		return channel, requests, err
	}
}

func (p *sessionPool) fill() {
	p.mu.Lock()
	if p.filling || p.closed {
		p.mu.Unlock()
		return
	}
	p.filling = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.filling = false; p.mu.Unlock() }()
	for {
		p.mu.Lock()
		stop := p.closed || len(p.idle) == cap(p.idle)
		p.mu.Unlock()
		if stop {
			return
		}
		channel, requests, err := p.client.OpenChannel("session", nil)
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			channel.Close()
			return
		}
		select {
		case p.idle <- idleSession{channel, requests}:
		default:
			channel.Close()
		}
		p.mu.Unlock()
	}
}

func (p *sessionPool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for {
		select {
		case session := <-p.idle:
			session.channel.Close()
		default:
			return
		}
	}
}
