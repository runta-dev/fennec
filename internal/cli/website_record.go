//go:build website_record

package cli

import (
	"context"
	"fennec/internal/launcher"
	"time"
)

// RecordWebsitePicker runs the production input loop and renderer against
// isolated presentation data. The build tag excludes this entry from releases.
// No real daemon session list or user session is accessible to this fixture.
func RecordWebsitePicker() error {
	sessions := []fenSession{
		{Receipt: launcher.LaunchReceipt{Agent: "codex", SessionID: "maple-river"}, Title: "Make webhook delivery idempotent", Status: "Idle"},
		{Receipt: launcher.LaunchReceipt{Agent: "claude", SessionID: "cedar-moon"}, Title: "Review API key authentication", Status: "Idle"},
		{Receipt: launcher.LaunchReceipt{Agent: "pi", SessionID: "birch-lake"}, Title: "Add retry backoff tests", Status: "Working"},
	}
	started := time.Now()
	_, err := fenPick(context.Background(), sessions, func() ([]fenSession, error) {
		updated := append([]fenSession(nil), sessions...)
		if time.Since(started) > 3*time.Second {
			updated[2].Status = "Idle"
		}
		return updated, nil
	}, func(fenSession) error { return nil }, func() error { return nil })
	return err
}
