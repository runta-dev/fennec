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
