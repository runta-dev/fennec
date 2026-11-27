package board

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ChangePoll is how often WaitChange looks at the board for a new change.
const ChangePoll = 50 * time.Millisecond

// MaxSyncWait caps how long a /v1/sync request may wait for a change (FEN-193).
const MaxSyncWait = 5 * time.Second

// ChangeStamp names the board's last materialized change. Every replicated
// write (a local commit or an applied sync) ends by rewriting
// replica-applied.json after its objects are on disk, so a reader woken by a
// new stamp sees the change. Writers in other processes (agents running
// `queue reply`) are seen too, since this is the file, not a channel.
//
// The file holds the journal's event count, which grows with every change, so
// the stamp is its contents plus its modification time: file times can be too
// coarse on their own to tell two quick writes apart.
func (s Store) ChangeStamp() string {
	path := filepath.Join(s.root(), "replica-applied.json")
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	count, err := os.ReadFile(path)
	if err != nil || len(count) > 32 {
		return ""
	}
	return fmt.Sprintf("%s:%d", bytes.TrimSpace(count), info.ModTime().UnixNano())
}

// WaitChange returns the board's stamp as soon as it differs from since, or
// after at most wait (or ctx ending) with the stamp unchanged. A stat every
// ChangePoll is what it costs; nothing is read until something changed.
func (s Store) WaitChange(ctx context.Context, since string, wait time.Duration) string {
	stamp := s.ChangeStamp()
	if stamp != since || wait <= 0 {
		return stamp
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(ChangePoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return stamp
		case <-deadline.C:
			return stamp
		case <-tick.C:
			if stamp = s.ChangeStamp(); stamp != since {
				return stamp
			}
		}
	}
}
