// Package cmuxhooks adapts cmux's per-invocation Codex hooks across a cloud TUI.
package cmuxhooks

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	shlex "github.com/anmitsu/go-shlex"
)

var eventCommands = map[string][]string{
	"SessionStart": {"session-start"}, "UserPromptSubmit": {"prompt-submit", "user-prompt-submit", "inbox-drain"},
	"Stop": {"stop", "inbox-stop"}, "PreToolUse": {"pre-tool-use"}, "PostToolUse": {"post-tool-use"},
	"PermissionRequest": {"notification"}, "Notification": {"notification"}, "SubagentStart": {"subagent-start"},
	"SubagentStop": {"subagent-stop"}, "SessionEnd": {"session-end", "session-finalize"},
}

func ValidEvent(event string) bool {
	for _, values := range eventCommands {
		for _, value := range values {
			if value == event {
				return true
			}
		}
	}
	return false
}

var generatedName = regexp.MustCompile(`^cmux-codex-hook-(?:([0-9a-f]{16})-)?([a-z-]+)\.sh$`)

func recognize(command, home string, allowed []string) (string, error) {
	parts, err := shlex.Split(command, true)
	if err != nil || len(parts) != 1 {
		return "", nil
	}
	path := parts[0]
	if !filepath.IsAbs(path) || filepath.Clean(filepath.Dir(path)) != filepath.Join(home, ".cmux/hooks") {
		return "", nil
	}
	match := generatedName.FindStringSubmatch(filepath.Base(path))
	if match == nil {
		return "", nil
	}
	event := match[2]
	found := false
	for _, candidate := range allowed {
		if candidate == event {
			found = true
		}
	}
	if !found {
		return "", nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return "", fmt.Errorf("cmux hook is unavailable on this Mac; restart the cmux terminal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read cmux hook")
	}
	if !strings.HasPrefix(string(data), "#!/bin/sh\n") || !strings.Contains(string(data), "CMUX_SURFACE_ID") {
		return "", nil
	}
	if match[1] != "" {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:8]) != match[1] {
			return "", fmt.Errorf("cmux hook changed after generation; restart the cmux terminal")
		}
	}
	return event, nil
}
