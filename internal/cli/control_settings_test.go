package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopSettingsImportDoesNotOverwriteCLIPreference(t *testing.T) {
	home := t.TempDir()
	settings := newDesktopSettingsWithMagpie(home, func() bool { return false })
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

func TestMagpieAbsentRequiresExplicitOptIn(t *testing.T) {
	home := t.TempDir()
	s := newDesktopSettingsWithMagpie(home, func() bool { return false })
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

func TestMagpieDetectionRequiresInstalledExecutable(t *testing.T) {
	home, apps := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config/magpie"), 0700); err != nil {
		t.Fatal(err)
	}
	if installedMagpie(home, apps, "") {
		t.Fatal("configuration residue treated as installation")
	}
	path := filepath.Join(apps, "Magpie.app/Contents/MacOS/magpie")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if installedMagpie(home, apps, "") {
		t.Fatal("non-executable file treated as installation")
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	if !installedMagpie(home, apps, "") {
		t.Fatal("installed application not detected")
	}
}

func TestInstalledMagpieDefaultPreservesExplicitChoice(t *testing.T) {
	home := t.TempDir()
	s := newDesktopSettingsWithMagpie(home, func() bool { return true })
	if !s.value.Magpie {
		t.Fatal("installed Magpie not enabled on first initialization")
	}
	if _, err := s.set(false)(context.Background(), json.RawMessage(`{"magpie":false}`)); err != nil {
		t.Fatal(err)
	}
	restored := newDesktopSettingsWithMagpie(home, func() bool { t.Fatal("saved preference was re-detected"); return true })
	if restored.value.Magpie {
		t.Fatal("explicit disable lost")
	}
}
func TestInitialDefaultIsPersistedWithoutOverwritingChoice(t *testing.T) {
	home := t.TempDir()
	s := newDesktopSettingsWithMagpie(home, func() bool { return false })
	if newDesktopSettingsWithMagpie(home, func() bool { t.Fatal("initial preference not saved"); return true }).value.Magpie {
		t.Fatal("default changed")
	}
	if _, err := s.set(false)(context.Background(), json.RawMessage(`{"magpie":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := initializeDesktopPreferences(s.path, desktopPreferences{Theme: "system", Language: "zh-CN"}); err != nil {
		t.Fatal(err)
	}
	if !newDesktopSettingsWithMagpie(home, func() bool { return false }).value.Magpie {
		t.Fatal("concurrent explicit choice replaced")
	}
}
