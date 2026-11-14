package updater

import (
	"context"
	"fennec/internal/connectivity"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// CheckInterval spaces background checks; one costs a single
	// unauthenticated GitHub API call (60 per hour per address).
	CheckInterval = time.Hour
	FreshAfter    = 5 * time.Minute
	// RetryAfterError retries a check that failed (offline, a release
	// being published) soon instead of a whole CheckInterval later.
	RetryAfterError = 5 * time.Minute
	rateWindow      = 5 * time.Second
)

type progressSample struct {
	at   time.Time
	done int64
}

// Snapshot is the public, UI/CLI-consumable state of the auto updater.
type Snapshot struct {
	State   string   `json:"state"`             // "" | "checking" | "latest" | "found" | "downloading" | "ready" | "available" | "source" | "error"
	Current string   `json:"current"`           // e.g. "0.2.11121023"
	Latest  string   `json:"latest,omitempty"`  // e.g. "0.2.11121200"
	Notes   string   `json:"notes,omitempty"`   // release notes markdown
	URL     string   `json:"url,omitempty"`     // release web page
	Stuck   string   `json:"stuck,omitempty"`   // why self-replacement is unavailable
	Step    string   `json:"step,omitempty"`    // download | retry | verify | reuse | unpack | signature
	Done    int64    `json:"done,omitempty"`    // downloaded bytes
	Total   int64    `json:"total,omitempty"`   // bytes this update downloads; reused components excluded
	Size    int64    `json:"size,omitempty"`    // bytes the update will download, when in found state
	Reused  []string `json:"reused,omitempty"`  // components taken from the installed app, e.g. "chromium"
	Saved   int64    `json:"saved,omitempty"`   // archive bytes not downloaded thanks to Reused
	Speed   int64    `json:"speed,omitempty"`   // bytes/sec over trailing 5s window
	Network bool     `json:"network,omitempty"` // true when download failed due to network
	Retry   bool     `json:"retry,omitempty"`   // true when user can retry download
	Error   string   `json:"error,omitempty"`   // sanitized human-readable error
}

// Config configures the daemon-owned auto updater service.
type Config struct {
	Bundle        string
	Current       string
	Feed          func() string
	Arch          string
	HTTPClient    *http.Client
	Transport     http.RoundTripper
	Network       *connectivity.Monitor
	AutoEnabled   func() bool
	VerifySigner  func(ctx context.Context, installedBundle, candidateBundle string) error
	ApplyUpdate   func(ctx context.Context, stagedApp, destinationBundle string) error
	CheckInterval time.Duration
}

// Service manages periodic background checks, staging, and transactional installation.
type Service struct {
	cfg Config

	mu         sync.Mutex
	state      string
	latest     *Release
	plan       UpdatePlan
	reused     []string
	staged     string
	stuck      string
	step       string
	done       int64
	total      int64
	rates      []progressSample
	networkErr bool
	retryable  bool
	errText    string
	askedAt    time.Time
	installing bool
}

func NewService(cfg Config) *Service {
	if cfg.Current == "" {
		cfg.Current = BundleVersion(cfg.Bundle)
	}
	if cfg.Feed == nil {
		cfg.Feed = FeedURL
	}
	if cfg.Arch == "" {
		cfg.Arch = TargetArch()
	}
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = CheckInterval
	}
	stuck := StuckReason(cfg.Bundle)
	return &Service{
		cfg:   cfg,
		stuck: stuck,
	}
}

// Start runs periodic background update checks while auto-update is enabled.
func (s *Service) Start(ctx context.Context) {
	if s.cfg.Bundle != "" && s.stuck == "" {
		CleanOldStaging(s.cfg.Bundle)
	}
	go func() {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for ctx.Err() == nil {
			if s.autoEnabled() && (s.cfg.Network == nil || s.cfg.Network.Online()) {
				s.mu.Lock()
				since := time.Since(s.askedAt)
				due := s.askedAt.IsZero() || since >= s.cfg.CheckInterval || (s.state == "error" && s.retryable && since >= RetryAfterError)
				s.mu.Unlock()
				if due {
					_ = s.Check(ctx, true)
				}
			}
			var netChanged <-chan struct{}
			if s.cfg.Network != nil {
				netChanged = s.cfg.Network.Changes()
			}
			select {
			case <-ctx.Done():
				return
			case <-netChanged:
			case <-ticker.C:
			}
		}
	}()
}

func (s *Service) autoEnabled() bool {
	if s.cfg.AutoEnabled == nil {
		return true
	}
	return s.cfg.AutoEnabled()
}

// Snapshot returns the current public update state; if refreshStale is true and
// the cached "latest" state is older than FreshAfter, it triggers an async check.
func (s *Service) Snapshot(ctx context.Context, refreshStale bool) Snapshot {
	s.mu.Lock()
	stale := refreshStale && s.autoEnabled() && (s.state == "latest" || (s.state == "error" && s.retryable)) && time.Since(s.askedAt) >= FreshAfter
	snap := s.snapshotLocked(time.Now())
	s.mu.Unlock()
	if stale && (s.cfg.Network == nil || s.cfg.Network.Online()) {
		go func() { _ = s.Check(ctx, true) }()
	}
	return snap
}

func (s *Service) snapshotLocked(now time.Time) Snapshot {
	snap := Snapshot{
		State:   s.state,
		Current: s.cfg.Current,
		Stuck:   s.stuck,
		Step:    s.step,
		Done:    s.done,
		Total:   s.total,
		Network: s.networkErr,
		Retry:   s.retryable,
		Error:   s.errText,
	}
	if s.latest != nil {
		snap.Latest = s.latest.Version
		snap.Notes = s.latest.Notes
		snap.URL = s.latest.URL
		snap.Size = s.plan.Download
		snap.Reused = s.plan.Reused
		snap.Saved = s.plan.Saved
		if s.state == "ready" && s.staged != "" {
			// What staging actually did, which can differ from the plan when
			// a reuse failed and the component was downloaded instead.
			snap.Reused = s.reused
			if len(s.reused) == 0 {
				snap.Saved = 0
			}
		}
	}
	if s.state == "downloading" {
		snap.Speed = speedFromSamples(s.rates, s.done, now)
	}
	return snap
}

// Check queries the release feed and, when downloadIfFound is true and the
// bundle is replaceable in place, downloads and stages the update.
func (s *Service) Check(ctx context.Context, downloadIfFound bool) Snapshot {
	s.mu.Lock()
	if s.state == "checking" || s.state == "downloading" || s.installing {
		snap := s.snapshotLocked(time.Now())
		s.mu.Unlock()
		return snap
	}
	if s.cfg.Network != nil && !s.cfg.Network.Online() {
		snap := s.snapshotLocked(time.Now())
		s.mu.Unlock()
		return snap
	}
	s.state = "checking"
	s.errText = ""
	s.networkErr = false
	s.retryable = false
	s.askedAt = time.Now()
	s.mu.Unlock()

	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	rel, err := FetchLatest(checkCtx, s.cfg.HTTPClient, s.cfg.Feed())
	var plan UpdatePlan
	if err == nil && Newer(rel.Version, s.cfg.Current) {
		// A missing or invalid component manifest only costs the full archive.
		if set, setErr := FetchComponents(checkCtx, s.cfg.HTTPClient, rel, s.cfg.Arch); setErr == nil {
			rel.Components = set
		}
		plan = PlanUpdate(rel, s.cfg.Bundle, s.cfg.Arch)
	}
	cancel()

	s.mu.Lock()
	if err != nil {
		// Preserve an already staged update if a transient recheck failed.
		if s.staged != "" && s.latest != nil {
			if _, statErr := os.Stat(s.staged); statErr == nil {
				s.state = "ready"
				snap := s.snapshotLocked(time.Now())
				s.mu.Unlock()
				return snap
			}
		}
		s.state = "error"
		s.networkErr = s.cfg.Network != nil && !s.cfg.Network.Refresh()
		s.retryable = true
		s.errText = "Unable to check for updates"
		log.Printf("update check: %v", err)
		snap := s.snapshotLocked(time.Now())
		s.mu.Unlock()
		return snap
	}

	// Keep an already staged newer/equal release if the CDN lagged behind.
	if s.latest != nil && Newer(s.latest.Version, rel.Version) {
		rel = s.latest
		plan = s.plan
	}
	if s.staged != "" && s.latest != nil && !Newer(rel.Version, s.latest.Version) {
		if _, statErr := os.Stat(s.staged); statErr == nil {
			if rel.Version == s.latest.Version && s.latest.Notes == "" && rel.Notes != "" {
				updated := *s.latest
				updated.Notes = rel.Notes
				s.latest = &updated
			}
			s.state = "ready"
			snap := s.snapshotLocked(time.Now())
			s.mu.Unlock()
			return snap
		}
		s.staged = ""
	}

	s.latest = rel
	s.plan = plan
	s.reused = nil
	switch {
	case !Released(s.cfg.Current):
		if Newer(rel.Version, s.cfg.Current) {
			s.state = "available"
			if s.stuck == "" {
				s.stuck = "development-build"
			}
		} else {
			s.state = "source"
		}
		snap := s.snapshotLocked(time.Now())
		s.mu.Unlock()
		return snap
	case !Newer(rel.Version, s.cfg.Current):
		s.state = "latest"
		snap := s.snapshotLocked(time.Now())
		s.mu.Unlock()
		return snap
	case s.cfg.Bundle == "" || s.stuck != "":
		s.state = "available"
		snap := s.snapshotLocked(time.Now())
		s.mu.Unlock()
		return snap
	case !downloadIfFound:
		s.state = "found"
		snap := s.snapshotLocked(time.Now())
		s.mu.Unlock()
		return snap
	}

	s.state = "downloading"
	s.done = 0
	s.total = 0
	s.step = StepDownload
	s.staged = ""
	s.rates = nil
	bundle := s.cfg.Bundle
	arch := s.cfg.Arch
	plan = s.plan
	s.mu.Unlock()

	return s.stageRelease(ctx, rel, bundle, arch, plan)
}

// Download stages a previously discovered update (or checks & downloads if needed).
func (s *Service) Download(ctx context.Context) Snapshot {
	s.mu.Lock()
	if s.state == "ready" && s.staged != "" {
		if _, err := os.Stat(s.staged); err == nil {
			snap := s.snapshotLocked(time.Now())
			s.mu.Unlock()
			return snap
		}
	}
	if s.state == "downloading" || s.state == "checking" || s.installing {
		snap := s.snapshotLocked(time.Now())
		s.mu.Unlock()
		return snap
	}
	rel := s.latest
	bundle := s.cfg.Bundle
	arch := s.cfg.Arch
	stuck := s.stuck
	if rel == nil || !Newer(rel.Version, s.cfg.Current) || bundle == "" || stuck != "" {
		s.mu.Unlock()
		return s.Check(ctx, true)
	}
	s.state = "downloading"
	s.done = 0
	s.total = 0
	s.step = StepDownload
	s.staged = ""
	s.errText = ""
	s.networkErr = false
	s.retryable = false
	s.rates = nil
	plan := s.plan
	s.mu.Unlock()

	return s.stageRelease(ctx, rel, bundle, arch, plan)
}

func (s *Service) stageRelease(ctx context.Context, rel *Release, bundle, arch string, plan UpdatePlan) Snapshot {
	staged, reused, err := stageReleaseBundle(ctx, s.cfg.Transport, rel, bundle, arch, plan, progressOptions{
		onProgress: func(done, total int64) {
			now := time.Now()
			s.mu.Lock()
			s.done = done
			s.total = total
			s.rates = recordProgressSample(s.rates, done, now)
			s.mu.Unlock()
		},
		onStep: func(step string) {
			s.mu.Lock()
			s.step = step
			s.mu.Unlock()
		},
	}, s.cfg.VerifySigner)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.state = "error"
		s.staged = ""
		s.retryable = true
		s.networkErr = IsNetworkError(err) || (s.cfg.Network != nil && !s.cfg.Network.Refresh())
		if s.networkErr {
			s.errText = "Download interrupted by network"
		} else {
			s.errText = "Could not verify downloaded update"
		}
		return s.snapshotLocked(time.Now())
	}
	s.state = "ready"
	s.step = ""
	s.staged = staged
	s.reused = reused
	s.errText = ""
	s.networkErr = false
	s.retryable = false
	return s.snapshotLocked(time.Now())
}

// Install applies a staged update (downloading first if needed) using ApplyUpdate.
func (s *Service) Install(ctx context.Context) (Snapshot, error) {
	snap := s.Download(ctx)
	s.mu.Lock()
	if s.installing {
		current := s.snapshotLocked(time.Now())
		s.mu.Unlock()
		return current, fmt.Errorf("update installation is already in progress")
	}
	if s.state != "ready" || s.staged == "" || s.cfg.Bundle == "" {
		current := s.snapshotLocked(time.Now())
		s.mu.Unlock()
		if current.Error != "" {
			return current, fmt.Errorf("%s", current.Error)
		}
		return current, fmt.Errorf("no verified update is ready to install")
	}
	if s.cfg.ApplyUpdate == nil {
		current := s.snapshotLocked(time.Now())
		s.mu.Unlock()
		return current, fmt.Errorf("update installer unavailable")
	}
	s.installing = true
	staged := s.staged
	bundle := s.cfg.Bundle
	s.mu.Unlock()

	// Materialize a temporary .app directory inside staging root so bundle
	// verifiers that require a .app extension accept it during atomic swap.
	stagedApp := filepath.Join(filepath.Dir(staged), "Fennec.app")
	_ = os.RemoveAll(stagedApp)
	if err := os.Rename(staged, stagedApp); err != nil {
		s.mu.Lock()
		s.installing = false
		s.state = "error"
		s.retryable = true
		s.errText = "Staged update unavailable"
		current := s.snapshotLocked(time.Now())
		s.mu.Unlock()
		return current, err
	}

	err := s.cfg.ApplyUpdate(ctx, stagedApp, bundle)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installing = false
	if err != nil {
		// Move back to non-.app staged name so the user can retry without re-downloading.
		_ = os.Rename(stagedApp, staged)
		s.errText = "Could not install update"
		s.retryable = true
		return s.snapshotLocked(time.Now()), err
	}
	_ = os.RemoveAll(filepath.Dir(staged))
	s.staged = ""
	snap = s.snapshotLocked(time.Now())
	return snap, nil
}

func recordProgressSample(samples []progressSample, done int64, now time.Time) []progressSample {
	if n := len(samples); n > 0 && done < samples[n-1].done {
		samples = nil
	}
	if n := len(samples); n > 0 && now.Sub(samples[n-1].at) < 250*time.Millisecond {
		return samples
	}
	i := 0
	for i+1 < len(samples) && now.Sub(samples[i+1].at) >= rateWindow {
		i++
	}
	return append(samples[i:], progressSample{at: now, done: done})
}

func speedFromSamples(samples []progressSample, done int64, now time.Time) int64 {
	for _, m := range samples {
		if d := now.Sub(m.at); d <= rateWindow+time.Second {
			if d < time.Second || done < m.done {
				return 0
			}
			return int64(float64(done-m.done) / d.Seconds())
		}
	}
	return 0
}
