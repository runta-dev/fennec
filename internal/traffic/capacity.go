// Capacity probing supplies a conservative budget hint, not an SSH path limit.
package traffic

import (
	"context"
	"errors"
	"fennec/internal/runtime"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"
)

// CapacityConfig bounds background measurements. MaxBudget is an independently
// chosen ceiling: a third-party upload cannot prove the capacity of an SSH path.
// There is deliberately no minimum rate that could exceed a slow measured link.
type CapacityConfig struct {
	ProbeBytes     int
	ProbeSamples   int
	ProbeTarget    string
	ProbeTimeout   time.Duration
	BaseInterval   time.Duration
	MaxInterval    time.Duration
	MaxBudget      int
	IdleFraction   float64
	IdleWindow     time.Duration
	SampleInterval time.Duration
}

func DefaultCapacityConfig() CapacityConfig {
	return CapacityConfig{
		ProbeBytes: 128 * 1024, ProbeSamples: 3, ProbeTarget: "https://speed.cloudflare.com/__up", ProbeTimeout: 30 * time.Second,
		// The current 4 Mbps egress profile retains 10% headroom. Unknown or
		// failed initial measurements still use the 128 KiB/s fallback.
		BaseInterval: time.Hour, MaxInterval: 24 * time.Hour, MaxBudget: 4_000_000 / 8 * 9 / 10,
		IdleFraction: 0.7, IdleWindow: time.Minute, SampleInterval: time.Second,
	}
}

func (c CapacityConfig) validate() error {
	u, err := url.Parse(c.ProbeTarget)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("capacity probe requires an HTTPS upload endpoint")
	}
	if c.ProbeBytes <= 0 || c.ProbeBytes > 4*1024*1024 || c.ProbeSamples < 1 || c.ProbeSamples > 5 || c.ProbeTimeout <= 0 ||
		c.BaseInterval <= 0 || c.MaxInterval < c.BaseInterval || c.MaxBudget < 2 ||
		c.IdleFraction <= 0 || c.IdleFraction > 1 || math.IsNaN(c.IdleFraction) ||
		c.SampleInterval <= 0 || c.IdleWindow < c.SampleInterval || c.IdleWindow/c.SampleInterval > 3600 {
		return errors.New("invalid capacity monitor configuration")
	}
	return nil
}

// CapacityMonitor serializes detection and publication. Failed uploads or target
// updates preserve the previous measurement and are eligible for retry.
type CapacityMonitor struct {
	mu        sync.Mutex
	detecting chan struct{}
	bps       int
	budget    int
	target    func(Config) error
	onChange  []func(Config)
	client    *http.Client
	sample    func() (uint64, bool)
}

func NewCapacityMonitor() *CapacityMonitor {
	return &CapacityMonitor{detecting: make(chan struct{}, 1), client: capacityHTTPClient(), sample: runtime.RecentTxBPS}
}

func (m *CapacityMonitor) OnChange(fn func(Config)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChange = append(m.onChange, fn)
}

func (m *CapacityMonitor) SetTarget(fn func(Config) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.target = fn
}

// Detected reports the last completed upload's payload throughput. Handshake,
// server acknowledgement and response time are included; this is not wire rate.
func (m *CapacityMonitor) Detected() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bps
}

func (m *CapacityMonitor) Detect(ctx context.Context, cfg CapacityConfig) (int, error) {
	if err := cfg.validate(); err != nil {
		return 0, err
	}
	select {
	case m.detecting <- struct{}{}:
		defer func() { <-m.detecting }()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.ProbeTimeout)
	defer cancel()
	measurements := make([]int, 0, cfg.ProbeSamples)
	for range cfg.ProbeSamples {
		bps, err := uploadCapacity(ctx, m.client, cfg)
		if err != nil {
			return 0, err
		}
		measurements = append(measurements, bps)
	}
	// One transient stall or unusually fast short upload must not set the
	// budget for an entire probe interval. Publish only one completed batch.
	slices.Sort(measurements)
	bps := measurements[(len(measurements)-1)/2]
	config := ConfigForCapacity(bps)
	if config.BytesPerSecond > cfg.MaxBudget {
		config.BytesPerSecond = cfg.MaxBudget
		config.BulkBytesPerSecond = bulkBudget(cfg.MaxBudget)
	}
	m.mu.Lock()
	target := m.target
	callbacks := append([]func(Config){}, m.onChange...)
	m.mu.Unlock()
	// Commit only after the owner accepts the update. Retrying the same result
	// after a failed target must not be suppressed as "unchanged".
	if target != nil {
		if err := target(config); err != nil {
			return 0, err
		}
	}
	m.mu.Lock()
	m.bps = bps
	m.budget = config.BytesPerSecond
	m.mu.Unlock()
	for _, fn := range callbacks {
		fn(config)
	}
	return bps, nil
}

// ConfigForCapacity leaves 10% measurement headroom and reserves up to 32 KiB/s
// (one quarter on slow links) for interactive traffic. Even sub-64 KiB/s links
// get a lower budget; packet size and burst limits do not impose a rate floor.
func ConfigForCapacity(bps int) Config {
	base := DefaultConfig()
	if bps <= 0 {
		return base
	}
	base.BytesPerSecond = max(1, bps-bps/10)
	base.BulkBytesPerSecond = bulkBudget(base.BytesPerSecond)
	return base
}

func bulkBudget(total int) int {
	reserve := min(InteractiveReserveBytesPerSecond, max(1, total/4))
	return max(1, total-reserve)
}

type egressSample struct {
	at    time.Time
	bps   uint64
	valid bool
}
type capacityHistory []egressSample

func (h *capacityHistory) record(now time.Time, bps uint64, valid bool, window time.Duration) {
	*h = append(*h, egressSample{now, bps, valid})
	first := 0
	for first < len(*h) && !(*h)[first].at.After(now.Add(-window)) {
		first++
	}
	*h = (*h)[first:]
}

func (h capacityHistory) idle(now time.Time, cfg CapacityConfig, known int) bool {
	if known <= 0 || len(h) < 2 || now.Sub(h[len(h)-1].at) > cfg.SampleInterval*2 {
		return false
	}
	var sum float64
	for _, s := range h {
		// Missing samples are unknown, never evidence of an idle link.
		if !s.valid {
			return false
		}
		sum += float64(s.bps)
	}
	return sum/float64(len(h)) < float64(known)*cfg.IdleFraction
}

// probePlan is driven by completion time, so slow/failed probes cannot cause a
// catch-up burst. Stable measurements and idle checks back off; changed results
// reset the interval. MaxInterval also bounds time without a real measurement.
type probePlan struct {
	next, lastProbe time.Time
	interval        time.Duration
	stable          int
	failed          bool
}

func (p *probePlan) backoff(cfg CapacityConfig) {
	p.interval = min(cfg.MaxInterval, p.interval+min(p.interval, cfg.MaxInterval-p.interval))
}

func (p *probePlan) complete(now time.Time, cfg CapacityConfig, before, after int, err error) {
	p.lastProbe = now
	p.failed = err != nil || after <= 0
	switch {
	case err != nil || before <= 0 || after <= 0:
		p.interval, p.stable = cfg.BaseInterval, 0
	case math.Abs(float64(after)-float64(before)) <= float64(before)*0.1:
		p.stable++
		if p.stable >= 3 {
			p.backoff(cfg)
			p.stable = 0
		}
	default:
		p.interval, p.stable = cfg.BaseInterval, 0
	}
	p.next = now.Add(p.interval)
}

func (p *probePlan) due(now time.Time, cfg CapacityConfig, idle bool) bool {
	if now.Before(p.next) {
		return false
	}
	if idle && !p.failed && !p.lastProbe.IsZero() && now.Sub(p.lastProbe) < cfg.MaxInterval {
		p.backoff(cfg)
		p.next = minTime(now.Add(p.interval), p.lastProbe.Add(cfg.MaxInterval))
		return false
	}
	return true
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Watch samples once a second independently of probe deadlines. Sampling must
// be running in this process; an unavailable sampler never suppresses retries.
func (m *CapacityMonitor) Watch(ctx context.Context, cfg CapacityConfig) {
	if cfg.validate() != nil {
		return
	}
	tick := time.NewTicker(cfg.SampleInterval)
	defer tick.Stop()
	plan := probePlan{interval: cfg.BaseInterval}
	var history capacityHistory
	probe := func() {
		before := m.Detected()
		bps, err := m.Detect(ctx, cfg)
		plan.complete(time.Now(), cfg, before, bps, err)
	}
	if ctx.Err() != nil {
		return
	}
	probe()
	timer := time.NewTimer(time.Until(plan.next))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			bps, valid := m.sample()
			history.record(now, bps, valid, cfg.IdleWindow)
		case <-timer.C:
			if ctx.Err() != nil {
				return
			}
			// Compare utilization with the applied ceiling, not a fast remote
			// endpoint's potentially much larger throughput measurement.
			m.mu.Lock()
			budget := m.budget
			m.mu.Unlock()
			if plan.due(time.Now(), cfg, history.idle(time.Now(), cfg, budget)) {
				probe()
			}
			timer.Reset(max(time.Nanosecond, time.Until(plan.next)))
		}
	}
}
