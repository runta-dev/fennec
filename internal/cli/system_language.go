package cli

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// systemPreferredLanguage is the user's first preferred language: macOS
// AppleLanguages, else $LANG.
var systemPreferredLanguage = func() string {
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if line = strings.Trim(strings.TrimSpace(line), `(),"`); line != "" {
					return line
				}
			}
		}
	}
	return os.Getenv("LANG")
}

// panelLanguage maps a system language tag (en-CN, zh-Hans-CN, zh_TW.UTF-8,
// ja-JP...) to a language the menu bar panel supports, English otherwise.
func panelLanguage(tag string) string {
	tag = strings.ToLower(strings.ReplaceAll(tag, "_", "-"))
	switch {
	case strings.HasPrefix(tag, "zh-hant"), strings.HasPrefix(tag, "zh-tw"), strings.HasPrefix(tag, "zh-hk"), strings.HasPrefix(tag, "zh-mo"):
		return "zh-TW"
	case strings.HasPrefix(tag, "zh"):
		return "zh-CN"
	case strings.HasPrefix(tag, "ja"):
		return "ja"
	case strings.HasPrefix(tag, "ko"):
		return "ko"
	case strings.HasPrefix(tag, "en-gb"):
		return "en-GB"
	}
	return "en-US"
}
