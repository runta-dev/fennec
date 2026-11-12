// Package audit persists a 7-day rolling, redacted local command and connector
// security audit log under ~/.local/share/fennec/audit/YYYY-MM-DD.jsonl.
package audit

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	RetentionDays   = 7
	MaxDailyBytes   = 32 << 20 // 32 MiB per daily shard cap
	DefaultPageSize = 100
	MaxPageSize     = 500
)

// Entry represents one audited edge action executed or blocked on this Mac.
type Entry struct {
	ID             string `json:"id"`
	TimestampMS    int64  `json:"timestamp_ms"`
	Kind           string `json:"kind"`    // "exec" | "sftp" | "tcp_forward" | "hook"
	Command        string `json:"command"` // redacted command or target summary
	Workdir        string `json:"workdir,omitempty"`
	SandboxMode    string `json:"sandbox_mode"` // "read-only" | "workspace" | "full"
	NetworkAllowed bool   `json:"network_allowed"`
	Decision       string `json:"decision"` // "auto_sandbox" | "user_approved" | "user_denied" | "sandbox_blocked"
	Blocked        bool   `json:"blocked"`
	ExitCode       int    `json:"exit_code"`
	DurationMS     int64  `json:"duration_ms"`
	Detail         string `json:"detail,omitempty"` // sanitized stderr snippet or block reason
}

// Summary provides top-level counts for the Settings badge and audit header.
type Summary struct {
	Total7d       int `json:"total_7d"`
	TodayCount    int `json:"today_count"`
	Blocked7d     int `json:"blocked_7d"`
	RetentionDays int `json:"retention_days"`
}

// QueryOptions filters audited events returned to UI or CLI callers.
type QueryOptions struct {
	Filter string `json:"filter"` // "" | "all" | "blocked" | "approved"
	Limit  int    `json:"limit"`
}

// QueryResult bundles recent entries (newest first) with 7-day summary counters.
type QueryResult struct {
	Summary Summary `json:"summary"`
	Entries []Entry `json:"entries"`
}

type Store struct {
	Directory string
	Now       func() time.Time
	mu        sync.Mutex
	lastPrune time.Time
}

func NewStore(home string) *Store {
	return &Store{
		Directory: filepath.Join(home, ".local/share/fennec/audit"),
		Now:       time.Now,
	}
}

var (
	dateShardPattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl$`)
	bearerSecretRe   = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)
	skTokenRe        = regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9_]{12,}|AIza[0-9A-Za-z_-]{20,})\b`)
	envSecretRe      = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_KEY|PRIVATE_KEY)=)([^\s'"]+|'[^']*'|"[^"]*")`)
)

// Redact scrubs common credential patterns and bounds length before persistence.
func Redact(raw string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 1024
	}
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = bearerSecretRe.ReplaceAllString(s, "${1}[REDACTED]")
	s = skTokenRe.ReplaceAllString(s, "[REDACTED]")
	s = envSecretRe.ReplaceAllString(s, "${1}[REDACTED]")
	if len(s) > maxChars {
		s = s[:maxChars] + "…"
	}
	return s
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Record appends a sanitized Entry to today's shard and prunes expired shards (>7d).
func (s *Store) Record(e Entry) error {
	if s == nil || s.Directory == "" {
		return nil
	}
	now := s.now().UTC()
	if e.ID == "" {
		var rnd [8]byte
		_, _ = rand.Read(rnd[:])
		e.ID = hex.EncodeToString(rnd[:])
	}
	if e.TimestampMS == 0 {
		e.TimestampMS = now.UnixMilli()
	}
	e.Command = Redact(e.Command, 1024)
	e.Detail = Redact(e.Detail, 384)
	if e.Decision == "user_denied" || e.Decision == "sandbox_blocked" {
		e.Blocked = true
	}

	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Directory, 0700); err != nil {
		return err
	}
	_ = os.Chmod(s.Directory, 0700)
	if s.lastPrune.IsZero() || now.Sub(s.lastPrune) >= time.Hour {
		s.pruneLocked(now)
		s.lastPrune = now
	}

	shard := filepath.Join(s.Directory, time.UnixMilli(e.TimestampMS).UTC().Format("2006-01-02")+".jsonl")
	if info, statErr := os.Stat(shard); statErr == nil && info.Size()+int64(len(line)) > MaxDailyBytes {
		return nil
	}
	f, err := os.OpenFile(shard, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(line)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// Prune removes daily audit shards older than 7 calendar days (UTC).
func (s *Store) Prune() {
	if s == nil || s.Directory == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now().UTC())
}

func cutoffDate(now time.Time) string {
	// Keep today plus the preceding 6 days = 7 days rolling window.
	return now.UTC().AddDate(0, 0, -(RetentionDays - 1)).Format("2006-01-02")
}

func (s *Store) pruneLocked(now time.Time) {
	entries, err := os.ReadDir(s.Directory)
	if err != nil {
		return
	}
	minDate := cutoffDate(now)
	for _, item := range entries {
		if item.IsDir() {
			continue
		}
		m := dateShardPattern.FindStringSubmatch(item.Name())
		if m == nil {
			continue
		}
		if m[1] < minDate {
			_ = os.Remove(filepath.Join(s.Directory, item.Name()))
		}
	}
}

// Query reads non-expired shards (pruning >7d first) and returns newest entries first.
func (s *Store) Query(opts QueryOptions) (QueryResult, error) {
	res := QueryResult{
		Summary: Summary{RetentionDays: RetentionDays},
		Entries: []Entry{},
	}
	if s == nil || s.Directory == "" {
		return res, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	filter := strings.ToLower(strings.TrimSpace(opts.Filter))

	now := s.now().UTC()
	todayStr := now.Format("2006-01-02")
	minDate := cutoffDate(now)

	s.mu.Lock()
	s.pruneLocked(now)
	dirEntries, err := os.ReadDir(s.Directory)
	if os.IsNotExist(err) {
		s.mu.Unlock()
		return res, nil
	}
	if err != nil {
		s.mu.Unlock()
		return res, err
	}
	var shards []string
	for _, item := range dirEntries {
		if item.IsDir() {
			continue
		}
		m := dateShardPattern.FindStringSubmatch(item.Name())
		if m != nil && m[1] >= minDate {
			shards = append(shards, item.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(shards)))

	for _, name := range shards {
		shardDate := strings.TrimSuffix(name, ".jsonl")
		raw, readErr := os.ReadFile(filepath.Join(s.Directory, name))
		if readErr != nil {
			continue
		}
		var dayEntries []Entry
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 4096), 256<<10)
		for scanner.Scan() {
			var e Entry
			if json.Unmarshal(scanner.Bytes(), &e) != nil {
				continue
			}
			res.Summary.Total7d++
			if shardDate == todayStr {
				res.Summary.TodayCount++
			}
			if e.Blocked {
				res.Summary.Blocked7d++
			}
			dayEntries = append(dayEntries, e)
		}
		for i := len(dayEntries) - 1; i >= 0; i-- {
			if len(res.Entries) >= limit {
				break
			}
			e := dayEntries[i]
			if matchFilter(e, filter) {
				res.Entries = append(res.Entries, e)
			}
		}
	}
	s.mu.Unlock()
	return res, nil
}

func matchFilter(e Entry, filter string) bool {
	switch filter {
	case "blocked":
		return e.Blocked
	case "approved":
		return !e.Blocked
	default:
		return true
	}
}

// Clear deletes all local audit log shards.
func (s *Store) Clear() error {
	if s == nil || s.Directory == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.Directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, item := range entries {
		if !item.IsDir() && strings.HasSuffix(item.Name(), ".jsonl") {
			_ = os.Remove(filepath.Join(s.Directory, item.Name()))
		}
	}
	return nil
}
