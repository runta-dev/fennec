package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"io"
	"os"
	"path/filepath"
	"sync"
)

type desktopWindowPreferences struct {
	NotificationSounds bool   `json:"notification_sounds"`
	Revision           uint64 `json:"revision"`
	Theme              string `json:"theme"`
	Language           string `json:"language"`
	FontSize           int    `json:"font_size"`
	PaddingX           int    `json:"padding_x"`
	PaddingY           int    `json:"padding_y"`
}
type desktopWindowSettings struct {
	mu    sync.Mutex
	path  string
	value desktopWindowPreferences
}

func newDesktopWindowSettings(home string) *desktopWindowSettings {
	s := &desktopWindowSettings{path: filepath.Join(home, ".config/fennec/desktop-settings.json"), value: desktopWindowPreferences{NotificationSounds: true, Theme: "system", Language: "system", FontSize: 14, PaddingX: 0, PaddingY: 0}}
	saved := s.value
	if data, err := os.ReadFile(s.path); err == nil && json.Unmarshal(data, &saved) == nil && validDesktopWindowPreferences(saved) {
		s.value = saved
	}
	return s
}
func validDesktopWindowPreferences(p desktopWindowPreferences) bool {
	return (p.Theme == "system" || p.Theme == "light" || p.Theme == "dark") && (p.Language == "system" || p.Language == "en-US" || p.Language == "zh-CN") && p.FontSize >= 8 && p.FontSize <= 32 && p.PaddingX >= 0 && p.PaddingX <= 32 && p.PaddingY >= 0 && p.PaddingY <= 32
}
func (s *desktopWindowSettings) get() desktopWindowPreferences {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}
func (s *desktopWindowSettings) set(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		NotificationSounds *bool   `json:"notification_sounds"`
		ExpectedRevision   *uint64 `json:"expected_revision"`
		Theme              *string `json:"theme"`
		Language           *string `json:"language"`
		FontSize           *int    `json:"font_size"`
		PaddingX           *int    `json:"padding_x"`
		PaddingY           *int    `json:"padding_y"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, &control.Fault{Code: "invalid_params", Message: "Check the settings"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ExpectedRevision != nil && *p.ExpectedRevision != s.value.Revision {
		return nil, &control.Fault{Code: "settings_changed", Message: "Settings changed"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	next := s.value
	if p.NotificationSounds != nil {
		next.NotificationSounds = *p.NotificationSounds
	}
	if p.Theme != nil {
		next.Theme = *p.Theme
	}
	if p.Language != nil {
		next.Language = *p.Language
	}
	if p.FontSize != nil {
		next.FontSize = *p.FontSize
	}
	if p.PaddingX != nil {
		next.PaddingX = *p.PaddingX
	}
	if p.PaddingY != nil {
		next.PaddingY = *p.PaddingY
	}
	if !validDesktopWindowPreferences(next) {
		return nil, &control.Fault{Code: "invalid_params", Message: "Check the settings"}
	}
	next.Revision++
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".desktop-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if err = json.NewEncoder(f).Encode(next); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Rename(f.Name(), s.path); err != nil {
		return nil, err
	}
	s.value = next
	return next, nil
}
func installDesktopWindowSettings(home string, c *controlplane.Controller) {
	installDesktopLayout(home, c)
	s := newDesktopWindowSettings(home)
	installDesktopActivity(home, c, s)
	c.Additional["desktop.settings.get"] = func(context.Context, json.RawMessage) (any, error) { return s.get(), nil }
	c.Additional["desktop.settings.set"] = s.set
}
