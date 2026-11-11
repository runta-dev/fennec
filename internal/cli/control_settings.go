package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/agent"
	"fennec/internal/control"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

type desktopSettings struct {
	changes sync.Mutex
	mu      sync.Mutex
	path    string
	value   desktopPreferences
}
type desktopPreferences struct {
	Revision    uint64 `json:"revision"`
	Magpie      bool   `json:"magpie"`
	Theme       string `json:"theme"`
	Language    string `json:"language"`
	Initialized bool   `json:"initialized"`
}

func newDesktopSettings(home string) *desktopSettings {
	return newDesktopSettingsWithMagpie(home, func() bool { return installedMagpie(home, "/Applications", agent.ExecutableSearchPath(home)) })
}
func newDesktopSettingsWithMagpie(home string, installed func() bool) *desktopSettings {
	settings := &desktopSettings{path: filepath.Join(home, ".config/fennec/settings.json"), value: desktopPreferences{Theme: "system", Language: "zh-CN"}}
	if _, err := os.Lstat(settings.path); os.IsNotExist(err) {
		settings.value.Magpie = installed()
		// Publish complete defaults once, without replacing a concurrent explicit choice.
		_ = initializeDesktopPreferences(settings.path, settings.value)
	}
	if raw, err := os.ReadFile(settings.path); err == nil {
		var value desktopPreferences
		if json.Unmarshal(raw, &value) == nil && validDesktop(value) {
			settings.value = value
		}
	}
	return settings
}
func installedMagpie(home, applications, searchPath string) bool {
	var candidates []string
	for _, root := range []string{applications, filepath.Join(home, "Applications")} {
		for _, name := range []string{"Magpie.app", "magpie.app"} {
			candidates = append(candidates, filepath.Join(root, name, "Contents/MacOS/magpie"), filepath.Join(root, name, "Contents/MacOS/Magpie"))
		}
	}
	for _, dir := range filepath.SplitList(searchPath) {
		if filepath.IsAbs(dir) {
			candidates = append(candidates, filepath.Join(dir, "magpie"))
		}
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return true
		}
	}
	return false
}
func initializeDesktopPreferences(path string, value desktopPreferences) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".settings-initial-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	data, _ := json.Marshal(value)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	err = os.Link(file.Name(), path)
	if os.IsExist(err) {
		return nil
	}
	return err
}
func validDesktop(value desktopPreferences) bool {
	return map[string]bool{"system": true, "light": true, "dark": true}[value.Theme] && map[string]bool{"zh-CN": true, "zh-TW": true, "en-US": true, "en-GB": true, "ja": true, "ko": true}[value.Language]
}
func (s *desktopSettings) snapshot() any { s.mu.Lock(); defer s.mu.Unlock(); return s.value }

// Keep persistence and reconciliation ordering consistent for concurrent clients.
// Otherwise two writers can both observe the old mode and the second can skip
// reconciliation even though it reverses the first writer's choice.
func (s *desktopSettings) update(ctx context.Context, raw json.RawMessage, changed func() error) (any, error) {
	s.changes.Lock()
	defer s.changes.Unlock()
	before := s.snapshot().(desktopPreferences).Magpie
	value, err := s.set(false)(ctx, raw)
	if err != nil {
		return nil, err
	}
	if before != value.(desktopPreferences).Magpie {
		if err := changed(); err != nil {
			return nil, err
		}
	}
	return value, nil
}
func (s *desktopSettings) set(importing bool) control.Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var request struct {
			ExpectedRevision *uint64 `json:"expected_revision"`
			Magpie           *bool   `json:"magpie"`
			Theme            *string `json:"theme"`
			Language         *string `json:"language"`
		}
		if json.Unmarshal(raw, &request) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid settings"}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if request.ExpectedRevision != nil && *request.ExpectedRevision != s.value.Revision {
			return nil, &control.Fault{Code: "settings_changed", Message: "Settings changed", Retryable: false}
		}
		if importing && s.value.Initialized {
			return s.value, nil
		}
		next := s.value
		if request.Magpie != nil && !importing {
			next.Magpie = *request.Magpie
		}
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
		if next == s.value {
			return s.value, nil
		}
		if next.Revision == ^uint64(0) {
			return nil, fmt.Errorf("settings revision exhausted")
		}
		next.Revision++
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
