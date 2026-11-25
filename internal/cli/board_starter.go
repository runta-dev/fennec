package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fennec/internal/localization"
	uiassets "fennec/native/macos/UI"
)

// starterQueueCopy is the name and description of the queue a new board starts
// with (THI-177), in the language the Fennec window shows.
func starterQueueCopy(home string) (name, description string) {
	catalog, err := localization.Decode(uiassets.Translations)
	if err != nil {
		return "My tasks", "Send what you need here. Agents take it in turn and reply to you. Delete this queue if you don't need it."
	}
	language := starterQueueLanguage(home)
	return catalog.Text("desktop.starter_queue_name", language), catalog.Text("desktop.starter_queue_description", language)
}

// starterQueueLanguage follows the window's language setting, then the
// system's first preferred language, as DesktopWindow.swift passes it.
func starterQueueLanguage(home string) string {
	var prefs struct {
		Language string `json:"language"`
	}
	if raw, err := os.ReadFile(filepath.Join(home, ".config/fennec/desktop-settings.json")); err == nil && json.Unmarshal(raw, &prefs) == nil {
		if prefs.Language == "zh-CN" || prefs.Language == "en-US" {
			return prefs.Language
		}
	}
	language := os.Getenv("LANG")
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if line = strings.Trim(strings.TrimSpace(line), `(),"`); line != "" {
					language = line
					break
				}
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(language), "zh") {
		return "zh-CN"
	}
	return "en-US"
}
