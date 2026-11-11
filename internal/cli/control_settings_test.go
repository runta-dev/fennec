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

func TestMagpieRequiresExplicitOptIn(t *testing.T) {
	home := t.TempDir()
	s := newDesktopSettings(home)
	if s.value.Magpie {
		t.Fatal("Magpie enabled by default")
	}
	if _, e := s.set(true)(context.Background(), json.RawMessage(`{"magpie":true}`)); e != nil {
		t.Fatal(e)
	}
	if s.value.Magpie {
		t.Fatal("legacy import implicitly enabled Magpie")
	}
	if _, e := s.set(false)(context.Background(), json.RawMessage(`{"magpie":true}`)); e != nil {
		t.Fatal(e)
	}
	if !newDesktopSettings(home).value.Magpie {
		t.Fatal("explicit choice not persisted")
	}
	if _, e := s.set(false)(context.Background(), json.RawMessage(`{"magpie":false}`)); e != nil {
		t.Fatal(e)
	}
	if newDesktopSettings(home).value.Magpie {
		t.Fatal("disable not persisted")
	}
}
