package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreRolling7DayRetentionRedactionAndFiltering(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	store := NewStore(home)
	store.Now = func() time.Time { return now }

	// Seed an 8-day-old shard (2026-11-04) that must be pruned, and a 6-day-old shard (2026-11-06) that must survive.
	if err := os.MkdirAll(store.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	expiredShard := filepath.Join(store.Directory, "2026-11-04.jsonl")
	if err := os.WriteFile(expiredShard, []byte(`{"id":"old","timestamp_ms":1793750400000,"kind":"exec","command":"echo stale","sandbox_mode":"workspace","decision":"auto_sandbox"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	sixDaysAgo := now.AddDate(0, 0, -6)
	if err := store.Record(Entry{
		TimestampMS: sixDaysAgo.UnixMilli(),
		Kind:        "exec",
		Command:     "git status",
		SandboxMode: "workspace",
		Decision:    "auto_sandbox",
		ExitCode:    0,
		DurationMS:  14,
	}); err != nil {
		t.Fatal(err)
	}

	// Record today's blocked command containing secret tokens that must be redacted.
	if err := store.Record(Entry{
		TimestampMS: now.UnixMilli(),
		Kind:        "exec",
		Command:     "OPENAI_API_KEY=sk-1234567890abcdef curl -H 'Authorization: Bearer secret-jwt-token-999' https://example.com",
		SandboxMode: "workspace",
		Decision:    "sandbox_blocked",
		ExitCode:    1,
		DurationMS:  9,
		Detail:      "sandbox-exec: network-outbound deny",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(expiredShard); !os.IsNotExist(err) {
		t.Fatal("expected >7 day audit shard 2026-11-04.jsonl to be pruned")
	}

	all, err := store.Query(QueryOptions{Filter: "all", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if all.Summary.Total7d != 2 || all.Summary.TodayCount != 1 || all.Summary.Blocked7d != 1 || len(all.Entries) != 2 {
		t.Fatalf("unexpected audit summary/entries: %+v", all)
	}
	latest := all.Entries[0]
	if strings.Contains(latest.Command, "sk-1234567890abcdef") || strings.Contains(latest.Command, "secret-jwt-token-999") {
		t.Fatalf("secrets were not redacted from audit log: %q", latest.Command)
	}
	if !latest.Blocked {
		t.Fatal("expected sandbox_blocked entry to mark Blocked=true")
	}

	blockedOnly, err := store.Query(QueryOptions{Filter: "blocked", Limit: 20})
	if err != nil || len(blockedOnly.Entries) != 1 || blockedOnly.Entries[0].Decision != "sandbox_blocked" {
		t.Fatalf("blocked filter failed: %+v %v", blockedOnly, err)
	}

	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	afterClear, err := store.Query(QueryOptions{})
	if err != nil || afterClear.Summary.Total7d != 0 || len(afterClear.Entries) != 0 {
		t.Fatalf("expected empty store after Clear, got %+v", afterClear)
	}
}
