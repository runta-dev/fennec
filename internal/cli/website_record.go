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
		{Receipt: launcher.LaunchReceipt{Agent: "codex", SessionID: "maple-river"}, Title: "Fix webhook deliveries on my Mac", Status: "Idle"},
		{Receipt: launcher.LaunchReceipt{Agent: "claude", SessionID: "cedar-moon"}, Title: "Review authorization on my Mac", Status: "Idle"},
		{Receipt: launcher.LaunchReceipt{Agent: "pi", SessionID: "birch-lake"}, Title: "Run retry tests on my Mac", Status: "Working"},
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
