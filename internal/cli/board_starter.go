package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

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
	language := systemPreferredLanguage()
	if strings.HasPrefix(strings.ToLower(language), "zh") {
		return "zh-CN"
	}
	return "en-US"
}
