package traffic

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfigForCapacityReservesInteractive(t *testing.T) {
	for _, capacity := range []int{2, 1000, 40 * 1024, 64 * 1024, 128 * 1024, 512 * 1024} {
		cfg := ConfigForCapacity(capacity)
		if cfg.BytesPerSecond > capacity || cfg.BytesPerSecond <= 0 || cfg.BulkBytesPerSecond <= 0 || cfg.BulkBytesPerSecond >= cfg.BytesPerSecond {
			t.Fatalf("invalid budget for %d: %+v", capacity, cfg)
		}
		if capacity >= 10 && cfg.BytesPerSecond > capacity-capacity/10 {
			t.Fatalf("no measurement headroom: %+v", cfg)
		}
		if _, err := New(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	if got := ConfigForCapacity(0); got != DefaultConfig() {
		t.Fatalf("unknown capacity: %+v", got)
	}
}

func probeServer(t *testing.T, handler http.HandlerFunc) (*CapacityMonitor, CapacityConfig) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	m := NewCapacityMonitor()
	m.client = server.Client()
	m.client.CheckRedirect = capacityHTTPClient().CheckRedirect
	cfg := DefaultCapacityConfig()
	cfg.ProbeSamples = 1
	cfg.ProbeTarget = server.URL
	cfg.ProbeTimeout = time.Second
	return m, cfg
}

func acknowledgeUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/octet-stream" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func TestCapacityWaitsForServerAcknowledgement(t *testing.T) {
	received, release := make(chan struct{}), make(chan struct{})
	m, cfg := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil || n != 128*1024 {
			t.Errorf("body: %d %v", n, err)
			return
		}
		close(received)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	var applied Config
	m.SetTarget(func(cfg Config) error { applied = cfg; return nil })
	done := make(chan error, 1)
	go func() { _, err := m.Detect(context.Background(), cfg); done <- err }()
	<-received
	select {
	case err := <-done:
		close(release)
		t.Fatalf("published before acknowledgement: %v", err)
	default:
	}
	if m.Detected() != 0 {
		t.Error("unconfirmed measurement published")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if applied.BytesPerSecond > cfg.MaxBudget || applied.BulkBytesPerSecond >= applied.BytesPerSecond {
		t.Fatalf("unsafe budget: %+v", applied)
	}
}

func TestCapacityUnreadTCPPeerCannotProduceMeasurement(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	m := NewCapacityMonitor()
	cfg := DefaultCapacityConfig()
	cfg.ProbeTarget = "https://" + listener.Addr().String()
	cfg.ProbeTimeout = 100 * time.Millisecond
	_, err = m.Detect(context.Background(), cfg)
	if err == nil || m.Detected() != 0 {
		t.Fatalf("unread peer accepted: %v", err)
	}
	select {
	case conn := <-accepted:
		conn.Close()
	case <-time.After(time.Second):
		t.Fatal("peer not connected")
	}
}

func TestCapacityFailuresPreserveBudget(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var reject atomic.Bool
			m, cfg := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
				if reject.Load() {
					w.Header().Set("Location", "https://example.invalid/")
					w.WriteHeader(status)
					return
				}
				acknowledgeUpload(w, r)
			})
			calls := 0
			m.SetTarget(func(Config) error { calls++; return nil })
			if _, err := m.Detect(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			before := m.Detected()
			reject.Store(true)
			if _, err := m.Detect(context.Background(), cfg); err == nil {
				t.Fatal("failed upload accepted")
			}
			if m.Detected() != before || calls != 1 {
				t.Fatalf("failure changed state: %d %d", m.Detected(), calls)
			}
		})
	}
}

func TestCapacityCancellationAfterUpload(t *testing.T) {
	received, release := make(chan struct{}), make(chan struct{})
	m, cfg := probeServer(t, func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); close(received); <-release })
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.Detect(ctx, cfg); done <- err }()
	<-received
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt acknowledgement wait")
	}
	if m.Detected() != 0 {
		t.Fatal("canceled upload published")
	}
}

func TestCapacityRejectsTruncatedAcknowledgement(t *testing.T) {
	m, cfg := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("short"))
	})
	if _, err := m.Detect(context.Background(), cfg); err == nil {
		t.Fatal("truncated response accepted")
	}
}

func TestCapacityTargetFailureCanRetry(t *testing.T) {
	m, cfg := probeServer(t, acknowledgeUpload)
	failed := true
	callbacks := 0
	m.SetTarget(func(Config) error {
		if failed {
			return errors.New("unavailable")
		}
		return nil
	})
	m.OnChange(func(Config) {
		callbacks++
		if m.Detected() == 0 {
			t.Error("callback before commit")
		}
	})
	if _, err := m.Detect(context.Background(), cfg); err == nil || m.Detected() != 0 || callbacks != 0 {
		t.Fatal("failed target committed")
	}
	failed = false
	if _, err := m.Detect(context.Background(), cfg); err != nil || m.Detected() <= 0 || callbacks != 1 {
		t.Fatalf("retry failed: %v", err)
	}
}

func TestCapacityConcurrentDetectionPublicationIsSerialized(t *testing.T) {
	m, cfg := probeServer(t, acknowledgeUpload)
	var active atomic.Int32
	m.SetTarget(func(Config) error {
		if active.Add(1) != 1 {
			t.Error("overlapping target updates")
		}
		time.Sleep(time.Millisecond)
		active.Add(-1)
		return nil
	})
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := m.Detect(context.Background(), cfg); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
}

func TestCapacityWatchHonorsProbeDeadline(t *testing.T) {
	var calls atomic.Int32
	m, cfg := probeServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); acknowledgeUpload(w, r) })
	cfg.SampleInterval = time.Millisecond
	cfg.IdleWindow = 10 * time.Millisecond
	m.sample = func() (uint64, bool) { return 0, false }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	published := make(chan struct{})
	m.OnChange(func(Config) {
		select {
		case <-published:
		default:
			close(published)
		}
	})
	done := make(chan struct{})
	go func() { defer close(done); m.Watch(ctx, cfg) }()
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("initial probe did not finish")
	}
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done
	if calls.Load() != 1 {
		t.Fatalf("one-hour interval ignored: %d probes", calls.Load())
	}
}

func TestProbePlanBackoffAndChangedMeasurement(t *testing.T) {
	cfg := DefaultCapacityConfig()
	now := time.Unix(100, 0)
	p := probePlan{interval: cfg.BaseInterval}
	p.complete(now, cfg, 0, 100, nil)
	if p.due(now.Add(time.Minute), cfg, false) {
		t.Fatal("probe before base interval")
	}
	for range 3 {
		now = now.Add(p.interval)
		p.complete(now, cfg, 100, 105, nil)
	}
	if p.interval != 2*cfg.BaseInterval {
		t.Fatalf("stable interval: %s", p.interval)
	}
	now = now.Add(p.interval)
	p.complete(now, cfg, 100, 200, nil)
	if p.interval != cfg.BaseInterval || p.stable != 0 {
		t.Fatal("changed capacity did not reset backoff")
	}
	p.interval = cfg.MaxInterval
	p.complete(now, cfg, 200, 0, errors.New("failed"))
	if p.interval != cfg.BaseInterval {
		t.Fatal("failure did not reset backoff")
	}
	p.interval = cfg.MaxInterval
	p.stable = 2
	p.complete(now, cfg, 100, 100, nil)
	if p.interval != cfg.MaxInterval {
		t.Fatal("backoff exceeded maximum")
	}
}

func TestIdleBackoffStillRechecksAtMaximumInterval(t *testing.T) {
	cfg := DefaultCapacityConfig()
	now := time.Unix(100, 0)
	p := probePlan{interval: cfg.BaseInterval}
	p.complete(now, cfg, 0, 100, nil)
	start := now
	for i := 0; i < 20; i++ {
		now = p.next
		if p.due(now, cfg, true) {
			if now.Sub(start) != cfg.MaxInterval {
				t.Fatalf("probe at %s", now.Sub(start))
			}
			return
		}
	}
	t.Fatal("idle link suppressed probes forever")
}

func TestCapacityHistoryDistinguishesUnknownAndIdle(t *testing.T) {
	cfg := DefaultCapacityConfig()
	now := time.Unix(100, 0)
	var h capacityHistory
	h.record(now, 0, true, cfg.IdleWindow)
	h.record(now.Add(time.Second), 0, true, cfg.IdleWindow)
	if !h.idle(now.Add(time.Second), cfg, 100) {
		t.Fatal("valid idle samples not recognized")
	}
	h.record(now.Add(2*time.Second), 0, false, cfg.IdleWindow)
	if h.idle(now.Add(2*time.Second), cfg, 100) {
		t.Fatal("missing telemetry treated as idle")
	}
	now = now.Add(2 * cfg.IdleWindow)
	h.record(now, 100, true, cfg.IdleWindow)
	h.record(now.Add(time.Second), 100, true, cfg.IdleWindow)
	if len(h) != 2 || h.idle(now.Add(time.Second), cfg, 100) {
		t.Fatal("history was not pruned or busy samples ignored")
	}
	if h.idle(now.Add(time.Minute), cfg, 100) {
		t.Fatal("stale samples treated as idle")
	}
}

func TestCapacityRejectsInvalidConfiguration(t *testing.T) {
	for _, mutate := range []func(*CapacityConfig){
		func(c *CapacityConfig) { c.ProbeTarget = "http://example.test" },
		func(c *CapacityConfig) { c.ProbeBytes = 0 }, func(c *CapacityConfig) { c.ProbeBytes = 5 * 1024 * 1024 },
		func(c *CapacityConfig) { c.SampleInterval = 0 }, func(c *CapacityConfig) { c.MaxBudget = 0 },
		func(c *CapacityConfig) { c.MaxInterval = time.Second },
	} {
		cfg := DefaultCapacityConfig()
		mutate(&cfg)
		if _, err := NewCapacityMonitor().Detect(context.Background(), cfg); err == nil {
			t.Fatalf("accepted invalid config: %+v", cfg)
		}
	}
}

func TestCapacityBudgetCeilingIsIndependentOfProbeThroughput(t *testing.T) {
	m, cfg := probeServer(t, acknowledgeUpload)
	cfg.MaxBudget = 4096
	var applied Config
	m.SetTarget(func(c Config) error { applied = c; return nil })
	bps, err := m.Detect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if applied.BytesPerSecond > cfg.MaxBudget || applied.BytesPerSecond > bps {
		t.Fatalf("unsafe cap: %+v", applied)
	}
	m.mu.Lock()
	budget := m.budget
	m.mu.Unlock()
	if budget != applied.BytesPerSecond {
		t.Fatal("utilization compared against raw probe throughput")
	}
}

func TestDefaultCapacitySupportsFourMegabitProfile(t *testing.T) {
	cfg := DefaultCapacityConfig()
	if cfg.MaxBudget != 450000 || cfg.MaxBudget <= DefaultConfig().BytesPerSecond {
		t.Fatalf("4 Mbps profile budget: %d", cfg.MaxBudget)
	}
	if cfg.ProbeSamples != 3 {
		t.Fatalf("sample count: %d", cfg.ProbeSamples)
	}
}

func TestCapacityPublishesOneCompletedBatch(t *testing.T) {
	var calls atomic.Int32
	m, cfg := probeServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); acknowledgeUpload(w, r) })
	cfg.ProbeSamples = 3
	updates := 0
	m.SetTarget(func(Config) error {
		updates++
		if calls.Load() != 3 {
			t.Error("published partial batch")
		}
		return nil
	})
	if _, err := m.Detect(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || updates != 1 {
		t.Fatalf("calls=%d updates=%d", calls.Load(), updates)
	}
}
