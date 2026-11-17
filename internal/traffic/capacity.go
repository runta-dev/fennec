// Capacity probing measures sustained host egress and adapts scheduler budgets.
package traffic

import (
	"context"
	"net"
	"sync"
	"time"
)

// CapacityConfig bounds adaptive budget updates. BytesPerSecond is the
// detected wire limit; BulkBytesPerSecond reserves room for urgent frames.
type CapacityConfig struct {
	ProbeBytes  int
	Interval    time.Duration
	MinBudget   int
	ProbeTarget string
}

func DefaultCapacityConfig() CapacityConfig {
	return CapacityConfig{
		ProbeBytes: 512 * 1024, Interval: 5 * time.Minute,
		MinBudget: 64 * 1024, ProbeTarget: "speed.cloudflare.com:443",
	}
}

// CapacityMonitor tracks the last measured host egress limit and lets owners
// subscribe to changes. Detect is safe for concurrent use.
type CapacityMonitor struct {
	mu       sync.Mutex
	bps      int
	target   func(Config) error
	onChange []func(Config)
}

func NewCapacityMonitor() *CapacityMonitor { return &CapacityMonitor{} }

// OnChange registers a callback invoked after each successful detection.
func (m *CapacityMonitor) OnChange(fn func(Config)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChange = append(m.onChange, fn)
}

// SetTarget registers the in-place scheduler update. The target is invoked
// only when a new measurement changes the detected capacity.
func (m *CapacityMonitor) SetTarget(fn func(Config) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.target = fn
}

// Detected reports the most recent measurement in bytes per second.
func (m *CapacityMonitor) Detected() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bps
}

// Detect uploads the probe payload over a fresh TCP connection and derives the
// wire capacity from successful payload bytes plus an 80-byte per-packet
// overhead estimate. A failed probe keeps the previous budget.
func (m *CapacityMonitor) Detect(ctx context.Context, cfg CapacityConfig) (int, error) {
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.ProbeTarget)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if err = conn.SetDeadline(deadline); err != nil {
		return 0, err
	}
	payload := make([]byte, 16*1024)
	started := time.Now()
	sent := 0
	for sent < cfg.ProbeBytes && time.Since(started) < 20*time.Second {
		n, writeErr := conn.Write(payload[:min(len(payload), cfg.ProbeBytes-sent)])
		sent += n
		if writeErr != nil {
			// A rate-limited reset still provides a bounded sample when enough
			// bytes completed; a connection that sends nothing stays unknown.
			if sent < 256*1024 {
				return 0, writeErr
			}
			break
		}
	}
	elapsed := time.Since(started).Seconds()
	if sent <= 0 || elapsed <= 0 {
		return 0, err
	}
	// A POST would carry headers, but a raw TLS ClientHello plus payload is
	// dominated by bulk bytes here. 4% overhead keeps the estimate conservative.
	wireBytes := float64(sent) * 1.04
	bps := int(wireBytes / elapsed)
	m.mu.Lock()
	changed := bps != m.bps
	m.bps = bps
	target := m.target
	callbacks := append([]func(Config){}, m.onChange...)
	m.mu.Unlock()
	if changed {
		if target != nil {
			if err := target(ConfigForCapacity(bps)); err != nil {
				return bps, err
			}
		}
		for _, fn := range callbacks {
			fn(ConfigForCapacity(bps))
		}
	}
	return bps, nil
}

// ConfigForCapacity converts a measured wire limit into scheduler budgets,
// leaving the interactive reserve available under sustained bulk load.
func ConfigForCapacity(bps int) Config {
	base := DefaultConfig()
	if bps <= 0 {
		return base
	}
	total := max(base.MinBudget(), bps)
	bulk := max(InteractiveReserveBytesPerSecond, total-InteractiveReserveBytesPerSecond)
	base.BytesPerSecond = total
	base.BulkBytesPerSecond = bulk
	return base
}

func (c Config) MinBudget() int { return c.BytesPerSecond }

// Watch periodically probes capacity and applies detected budgets until the
// context ends. The first probe runs immediately.
func (m *CapacityMonitor) Watch(ctx context.Context, cfg CapacityConfig) {
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, _ = m.Detect(probeCtx, cfg)
		cancel()
		timer := time.NewTimer(cfg.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
