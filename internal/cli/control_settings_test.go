package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
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

func TestSettingsRevisionPreventsStaleRestoration(t *testing.T) {
	home := t.TempDir()
	s := newDesktopSettingsWithMagpie(home, func() bool { return false })
	apply := func(raw string) (desktopPreferences, error) {
		v, err := s.set(false)(context.Background(), json.RawMessage(raw))
		if err != nil {
			return desktopPreferences{}, err
		}
		return v.(desktopPreferences), nil
	}
	first, err := apply(`{"magpie":true,"expected_revision":0}`)
	if err != nil || first.Revision != 1 {
		t.Fatal(first, err)
	}
	// A user turns the switch off and on again while a profiler is running.
	if _, err = apply(`{"magpie":false}`); err != nil {
		t.Fatal(err)
	}
	latest, err := apply(`{"magpie":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = apply(`{"magpie":false,"expected_revision":1}`); err == nil {
		t.Fatal("stale restoration overwrote user's choice")
	}
	restored := newDesktopSettings(home).value
	if !restored.Magpie || restored.Revision != latest.Revision {
		t.Fatal(restored)
	}
	same, err := apply(`{"magpie":true}`)
	if err != nil || same.Revision != latest.Revision {
		t.Fatal("no-op advanced revision", same, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.set(false)(ctx, json.RawMessage(`{"magpie":false}`)); err == nil {
		t.Fatal("canceled write persisted")
	}
	if s.value != same {
		t.Fatal("canceled write changed settings")
	}
}

func TestSettingsUpdateSerializesPersistenceAndReconciliation(t *testing.T) {
	s := newDesktopSettingsWithMagpie(t.TempDir(), func() bool { return false })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := s.update(ctx, json.RawMessage(`{"magpie":true}`), func() error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		first <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first update did not enter")
	}
	secondStarted, second := make(chan struct{}), make(chan error, 1)
	var reconciled atomic.Bool
	go func() {
		close(secondStarted)
		_, err := s.update(ctx, json.RawMessage(`{"magpie":false}`), func() error { reconciled.Store(true); return nil })
		second <- err
	}()
	<-secondStarted
	// The first callback must continue to observe its own persisted choice.
	if !s.snapshot().(desktopPreferences).Magpie {
		t.Fatal("second update overtook first reconciliation")
	}
	close(release)
	for _, done := range []chan error{first, second} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("update did not finish")
		}
	}
	if !reconciled.Load() || s.snapshot().(desktopPreferences).Magpie {
		t.Fatal("final choice was not reconciled")
	}
}
