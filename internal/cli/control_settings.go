package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

type desktopSettings struct {
	mu    sync.Mutex
	path  string
	value desktopPreferences
}
type desktopPreferences struct {
	Theme       string `json:"theme"`
	Language    string `json:"language"`
	Initialized bool   `json:"initialized"`
}

func newDesktopSettings(home string) *desktopSettings {
	settings := &desktopSettings{path: filepath.Join(home, ".config/fennec/settings.json"), value: desktopPreferences{Theme: "system", Language: "zh-CN"}}
	if raw, err := os.ReadFile(settings.path); err == nil {
		var value desktopPreferences
		if json.Unmarshal(raw, &value) == nil && validDesktop(value) {
			settings.value = value
		}
	}
	return settings
}
func validDesktop(value desktopPreferences) bool {
	return map[string]bool{"system": true, "light": true, "dark": true}[value.Theme] && map[string]bool{"zh-CN": true, "zh-TW": true, "en-US": true, "en-GB": true, "ja": true, "ko": true}[value.Language]
}
func (s *desktopSettings) snapshot() any { s.mu.Lock(); defer s.mu.Unlock(); return s.value }
func (s *desktopSettings) set(importing bool) control.Handler {
	return func(_ context.Context, raw json.RawMessage) (any, error) {
		var request struct {
			Theme    *string `json:"theme"`
			Language *string `json:"language"`
		}
		if json.Unmarshal(raw, &request) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid settings"}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if importing && s.value.Initialized {
			return s.value, nil
		}
		next := s.value
		if request.Theme != nil {
			next.Theme = *request.Theme
		}
		if request.Language != nil {
			next.Language = *request.Language
		}
		next.Initialized = true
		if !validDesktop(next) {
			return nil, &control.Fault{Code: "invalid_params", Message: "Unsupported theme or language"}
		}
		if err := writePublicJSON(s.path, next); err != nil {
			return nil, err
		}
		s.value = next
		return next, nil
	}
}
func platformLoginItem(ctx context.Context, operation string) (any, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, filepath.Join(filepath.Dir(binary), "Fennec"), "--platform-login-item", operation)
	raw, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("login item platform operation failed")
	}
	var result map[string]any
	if len(raw) > 65536 || json.Unmarshal(raw, &result) != nil {
		return nil, fmt.Errorf("invalid platform response")
	}
	if result["error"] == true {
		return nil, &control.Fault{Code: "login_item_failed", Message: "Unable to update login item; check macOS Login Items"}
	}
	return result, nil
}
