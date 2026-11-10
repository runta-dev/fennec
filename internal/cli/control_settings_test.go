package cli

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDesktopSettingsImportDoesNotOverwriteCLIPreference(t *testing.T) {
	home := t.TempDir()
	settings := newDesktopSettings(home)
	if _, err := settings.set(false)(context.Background(), json.RawMessage(`{"language":"en-US","theme":"dark"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.set(true)(context.Background(), json.RawMessage(`{"language":"zh-CN","theme":"light"}`)); err != nil {
		t.Fatal(err)
	}
	restored := newDesktopSettings(home).snapshot().(desktopPreferences)
	if restored.Language != "en-US" || restored.Theme != "dark" {
		t.Fatal(restored)
	}
	if _, err := settings.set(false)(context.Background(), json.RawMessage(`{"theme":"invalid"}`)); err == nil {
		t.Fatal("invalid settings accepted")
	}
}
