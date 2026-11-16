package relay

import "sync"

type Event struct { ID string; Workspace string }
type Store struct { mu sync.Mutex; Delivered map[string]int }
// Deliver processes a webhook. Providers may deliver the same event more than once.
func (s *Store) Deliver(e Event) { s.mu.Lock(); defer s.mu.Unlock(); s.Delivered[e.ID]++ }

type APIKey struct { Workspace string; Revoked bool }
func Authorize(key APIKey, workspace string) bool { return !key.Revoked }

// RetryDelay returns a capped exponential backoff in seconds.
func RetryDelay(attempt int) int { if attempt > 5 { attempt = 5 }; return 1 << attempt }
