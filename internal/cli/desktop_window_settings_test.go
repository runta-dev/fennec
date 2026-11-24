package cli

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestDesktopWindowSettingsAreIndependent(t *testing.T) {
	home := t.TempDir()
	menu := newDesktopSettings(home)
	if _, err := menu.set(false)(context.Background(), json.RawMessage(`{"theme":"light","language":"en-US"}`)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(menu.path)
	if err != nil {
		t.Fatal(err)
	}
	window := newDesktopWindowSettings(home)
	if _, err = window.set(context.Background(), json.RawMessage(`{"theme":"dark","language":"zh-CN","font_size":16}`)); err != nil {
		t.Fatal(err)
	}
	loaded := newDesktopWindowSettings(home).get()
	if loaded.Theme != "dark" || loaded.Language != "zh-CN" || loaded.FontSize != 16 {
		t.Fatal(loaded)
	}
	after, err := os.ReadFile(menu.path)
	if err != nil || string(before) != string(after) {
		t.Fatal("desktop preferences changed menu bar settings")
	}
	if _, err = window.set(context.Background(), json.RawMessage(`{"magpie":false}`)); err == nil {
		t.Fatal("accepted a menu bar setting in the app namespace")
	}
	if _, err = window.set(context.Background(), json.RawMessage(`{"font_size":999}`)); err == nil {
		t.Fatal("accepted invalid font size")
	}
	if _, err = window.set(context.Background(), json.RawMessage(`{"expected_revision":0,"theme":"light"}`)); err == nil {
		t.Fatal("accepted stale write")
	}
	if newDesktopWindowSettings(home).get() != loaded {
		t.Fatal("failed write changed preferences")
	}
}

func TestSidebarWidthPersistsWithoutResettingExistingPreferences(t *testing.T) {
	home := t.TempDir()
	s := newDesktopWindowSettings(home)
	if s.get().SidebarWidth != 252 {
		t.Fatal(s.get())
	}
	if _, err := s.set(context.Background(), json.RawMessage(`{"sidebar_width":360,"theme":"dark"}`)); err != nil {
		t.Fatal(err)
	}
	loaded := newDesktopWindowSettings(home).get()
	if loaded.SidebarWidth != 360 || loaded.Theme != "dark" {
		t.Fatal(loaded)
	}
	for _, body := range []string{`{"sidebar_width":179}`, `{"sidebar_width":481}`} {
		if _, err := s.set(context.Background(), json.RawMessage(body)); err == nil {
			t.Fatal("invalid width accepted")
		}
	}
	if s.get() != loaded {
		t.Fatal("rejected resize changed preferences")
	}
	if err := os.WriteFile(s.path, []byte(`{"theme":"dark","font_size":16}`), 0600); err != nil {
		t.Fatal(err)
	}
	old := newDesktopWindowSettings(home).get()
	if old.SidebarWidth != 252 || old.Theme != "dark" || old.FontSize != 16 {
		t.Fatal("old preferences were reset", old)
	}
}
