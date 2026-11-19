package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/audit"
	"testing"
)

func TestGetStartedCompletionSurvivesStaleReset(t *testing.T) {
	home := t.TempDir()
	settings := newDesktopSettings(home)
	if settings.value.GetStartedDone {
		t.Fatal("new users must start incomplete")
	}
	for _, raw := range []string{`{"get_started_done":true,"get_started_hidden":true}`, `{"get_started_done":false}`, `{"theme":"dark"}`} {
		if _, err := settings.set(false)(context.Background(), json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	restored := newDesktopSettings(home)
	if !restored.value.GetStartedDone {
		t.Fatal("a stale client cleared completed onboarding")
	}
	view := desktopSettingsResponse(home, restored.value, audit.Summary{})
	if view["get_started_done"] != true || view["get_started_hidden"] != true {
		t.Fatalf("UI settings projection lost onboarding flags: %v", view)
	}
	if restored.value.Theme != "dark" {
		t.Fatal("unrelated settings were not saved")
	}
}
