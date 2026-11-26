package agentcmd

import "path/filepath"

// BinaryName keeps product IDs separate from executable names. Never intercept
// Cursor's editor (`cursor`); `agent` is handled through CursorAlias.
func BinaryName(id string) string {
	if id == "cursor" {
		return "cursor-agent"
	}
	return id
}

// CursorAlias is the short name Cursor's installer publishes beside
// cursor-agent. Other vendors ship an `agent` too, so Fennec takes it over only
// when it resolves to Cursor's CLI and runs any other `agent` unchanged.
const CursorAlias = "agent"

// IsCursorCLI reports whether path resolves to Cursor's CLI, whose installed
// executable is named cursor-agent behind any symlinks.
func IsCursorCLI(path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && filepath.Base(resolved) == "cursor-agent"
}

// NativeOnly agents use their own accounts even when Magpie serves other agents.
// agy is one by choice: Magpie would serve it by reverse-proxying Google's Gemini
// service, which can get the Google account suspended (THI-123).
func NativeOnly(id string) bool { return id == "cursor" || id == "agy" }
