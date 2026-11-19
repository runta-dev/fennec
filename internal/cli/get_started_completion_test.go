package cli

import (
	"context"
	"encoding/json"
	"testing"
)

func TestGetStartedCompletionSurvivesStaleReset(t *testing.T) {
	home := t.TempDir()
	settings := newDesktopSettings(home)
	if settings.value.GetStartedDone {
		t.Fatal("new users must start incomplete")
	}
	for _, raw := range []string{`{"get_started_done":true}`, `{"get_started_done":false}`, `{"theme":"dark"}`} {
		if _, err := settings.set(false)(context.Background(), json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	restored := newDesktopSettings(home)
	if !restored.value.GetStartedDone {
		t.Fatal("a stale client cleared completed onboarding")
	}
	if restored.value.Theme != "dark" {
		t.Fatal("unrelated settings were not saved")
	}
}
