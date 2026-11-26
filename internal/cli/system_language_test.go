package cli

import "testing"

func TestPanelLanguageFollowsSystem(t *testing.T) {
	for tag, want := range map[string]string{
		"en-CN": "en-US", "en-US": "en-US", "en-GB": "en-GB", "en_GB.UTF-8": "en-GB",
		"zh-Hans-CN": "zh-CN", "zh_CN.UTF-8": "zh-CN", "zh-Hant-TW": "zh-TW", "zh-HK": "zh-TW",
		"ja-JP": "ja", "ko-KR": "ko", "de-DE": "en-US", "": "en-US", "C.UTF-8": "en-US",
	} {
		if got := panelLanguage(tag); got != want {
			t.Errorf("panelLanguage(%q) = %q, want %q", tag, got, want)
		}
	}
}

// FEN-29: a Mac whose system language is English must not start in Chinese.
func TestNewSettingsStartInSystemLanguage(t *testing.T) {
	previous := systemPreferredLanguage
	t.Cleanup(func() { systemPreferredLanguage = previous })
	for system, want := range map[string]string{"en-CN": "en-US", "zh-Hans-CN": "zh-CN"} {
		systemPreferredLanguage = func() string { return system }
		settings := newDesktopSettingsWithMagpie(t.TempDir(), func() bool { return false })
		if got := settings.snapshot().(desktopPreferences).Language; got != want {
			t.Errorf("system %q: new settings language = %q, want %q", system, got, want)
		}
	}
	home := t.TempDir()
	systemPreferredLanguage = func() string { return "zh-Hans-CN" }
	newDesktopSettingsWithMagpie(home, func() bool { return false })
	systemPreferredLanguage = func() string { return "en-US" }
	if got := newDesktopSettingsWithMagpie(home, func() bool { return false }).snapshot().(desktopPreferences).Language; got != "zh-CN" {
		t.Errorf("saved language replaced by system language: %q", got)
	}
}
