// Capacity probing measures sustained host egress and adapts scheduler budgets.
package traffic

import (
	"context"
	"fennec/internal/runtime"
	"net"
	"sync"
	"time"
)

// CapacityConfig bounds adaptive budget updates. BytesPerSecond is the
// detected wire limit; BulkBytesPerSecond reserves room for urgent frames.
type CapacityConfig struct {
	ProbeBytes   int
	BaseInterval time.Duration
	MaxInterval  time.Duration
	MinBudget    int
	ProbeTarget  string
	IdleFraction float64
	IdleWindow   time.Duration
}

func DefaultCapacityConfig() CapacityConfig {
	return CapacityConfig{
		ProbeBytes: 128 * 1024, BaseInterval: time.Hour, MaxInterval: 24 * time.Hour,
		MinBudget: 64 * 1024, ProbeTarget: "speed.cloudflare.com:443",
		IdleFraction: 0.7, IdleWindow: time.Minute,
	}
}

// CapacityMonitor tracks the last measured host egress limit and lets owners
// subscribe to changes. Detect is safe for concurrent use.
type CapacityMonitor struct {
	mu           sync.Mutex
	bps          int
	target       func(Config) error
	onChange     []func(Config)
	idleWindow   []uint64
	idleWindowAt time.Time
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

// shouldProbe reports whether a fresh probe is worth its traffic. Links with
// no known capacity always probe; known-capacity links probe only when recent
// egress was high enough that the limit may have changed.
func (m *CapacityMonitor) shouldProbe(cfg CapacityConfig) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	known := m.bps
	now := time.Now()
	if m.idleWindowAt.IsZero() || now.Sub(m.idleWindowAt) > cfg.IdleWindow*2 {
		m.idleWindow = m.idleWindow[:0]
	}
	m.idleWindowAt = now
	if known <= 0 || len(m.idleWindow) < 2 {
		return true
	}
	var total uint64
	for _, sample := range m.idleWindow {
		total += sample
	}
	average := float64(total) / float64(len(m.idleWindow))
	return average > float64(known)*cfg.IdleFraction
}

// recordIdleSample tracks recent egress to decide whether the next scheduled
// probe is needed.
func (m *CapacityMonitor) recordIdleSample() {
	bps := runtime.RecentTxBPS()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idleWindow = append(m.idleWindow, bps)
	if len(m.idleWindow) > 60 {
		m.idleWindow = m.idleWindow[len(m.idleWindow)-60:]
	}
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
			if sent < cfg.ProbeBytes/2 {
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

// Watch samples recent egress once per minute and probes only when the link
// looks loaded or has no known capacity. Stable results double the interval up
// to MaxInterval; a new result resets it so changed host limits converge fast.
func (m *CapacityMonitor) Watch(ctx context.Context, cfg CapacityConfig) {
	tick := time.NewTicker(cfg.IdleWindow)
	defer tick.Stop()
	interval := cfg.BaseInterval
	stable := 0
	probe := func() {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		bps, err := m.Detect(probeCtx, cfg)
		cancel()
		switch {
		case err != nil || bps <= 0:
			interval, stable = cfg.BaseInterval, 0
		case bps == m.Detected():
			stable++
			if stable >= 3 {
				interval, stable = min(cfg.MaxInterval, interval*2), 0
			}
		default:
			interval, stable = cfg.BaseInterval, 0
		}
	}
	probe()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.recordIdleSample()
			if m.shouldProbe(cfg) {
				probe()
			}
		}
	}
}
