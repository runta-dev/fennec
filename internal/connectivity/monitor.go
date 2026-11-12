// Package connectivity tracks local default-route availability so background
// services pause while the Mac is offline and resume automatically on reconnect.
package connectivity

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

var ErrOffline = errors.New("network is offline")

// ProbeRoutingTable checks whether the kernel has a non-loopback route to the
// public internet by binding UDP sockets without transmitting any packets.
func ProbeRoutingTable() bool {
	for _, target := range []struct {
		network string
		address string
	}{
		{"udp4", "1.1.1.1:443"},
		{"udp6", "[2606:4700:4700::1111]:443"},
	} {
		conn, err := net.DialTimeout(target.network, target.address, 200*time.Millisecond)
		if err != nil {
			continue
		}
		local, _ := conn.LocalAddr().(*net.UDPAddr)
		_ = conn.Close()
		if local != nil && len(local.IP) > 0 && !local.IP.IsUnspecified() && !local.IP.IsLoopback() {
			return true
		}
	}
	return false
}

// IsOfflineError reports whether err indicates local network unavailability
// rather than an application, authentication, or remote runtime fault.
func IsOfflineError(err error, online bool) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrOffline) {
		return true
	}
	if !online {
		return true
	}
	for _, code := range []syscall.Errno{
		syscall.ENETDOWN,
		syscall.ENETUNREACH,
		syscall.EHOSTDOWN,
		syscall.EHOSTUNREACH,
		syscall.EADDRNOTAVAIL,
	} {
		if errors.Is(err, code) {
			return true
		}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return !ProbeRoutingTable()
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"network is down",
		"network is unreachable",
		"no route to host",
		"can't assign requested address",
		"cannot assign requested address",
		"network is offline",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

type Monitor struct {
	probe    func() bool
	interval time.Duration

	mu      sync.Mutex
	started bool
	online  bool
	changed chan struct{}
}

func NewMonitor(probe func() bool, interval time.Duration) *Monitor {
	if probe == nil {
		probe = ProbeRoutingTable
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Monitor{
		probe:    probe,
		interval: interval,
		online:   probe(),
		changed:  make(chan struct{}),
	}
}

func (m *Monitor) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.online = m.probe()
	m.mu.Unlock()

	go func() {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.Refresh()
			}
		}
	}()
}

// Refresh re-evaluates local route availability immediately and notifies
// subscribers if the online state changed.
func (m *Monitor) Refresh() bool {
	if m == nil {
		return ProbeRoutingTable()
	}
	now := m.probe()
	m.mu.Lock()
	defer m.mu.Unlock()
	if now != m.online {
		m.online = now
		close(m.changed)
		m.changed = make(chan struct{})
	}
	return m.online
}

func (m *Monitor) Online() bool {
	if m == nil {
		return ProbeRoutingTable()
	}
	m.mu.Lock()
	started := m.started
	online := m.online
	m.mu.Unlock()
	if !started {
		return m.Refresh()
	}
	return online
}

func (m *Monitor) Changes() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.changed
}

// WaitOnline blocks until local connectivity is available or ctx is canceled.
func (m *Monitor) WaitOnline(ctx context.Context) error {
	if m == nil {
		return ctx.Err()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		changed := m.Changes()
		if m.Refresh() {
			return nil
		}
		timer := time.NewTimer(m.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}
