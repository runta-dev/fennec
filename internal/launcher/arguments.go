package launcher

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResolveLaunchDirectory consumes Codex's documented cwd option before project
// mapping. Additional local roots require explicit sync-set registration first.
// Arguments after -- remain literal prompt content, never parsed as options.
func ResolveLaunchDirectory(agent, cwd string, args []string) (string, []string, error) {
	if !Supported[agent] || !filepath.IsAbs(cwd) {
		return "", nil, fmt.Errorf("supported agent and absolute cwd required")
	}
	result := []string{}
	directory := cwd
	override := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			result = append(result, args[i:]...)
			break
		}
		if arg == "--add-dir" || strings.HasPrefix(arg, "--add-dir=") {
			return "", nil, fmt.Errorf("additional local directories must be registered for cloud sync before launch")
		}
		if agent == "codex" && (arg == "-C" || arg == "--cd" || strings.HasPrefix(arg, "--cd=") || strings.HasPrefix(arg, "-C") && len(arg) > 2) {
			if override {
				return "", nil, fmt.Errorf("multiple working-directory overrides")
			}
			override = true
			value := ""
			if strings.HasPrefix(arg, "--cd=") {
				value = strings.TrimPrefix(arg, "--cd=")
			} else if strings.HasPrefix(arg, "-C") && len(arg) > 2 {
				value = strings.TrimPrefix(strings.TrimPrefix(arg, "-C"), "=")
			} else {
				i++
				if i >= len(args) {
					return "", nil, fmt.Errorf("working-directory argument missing")
				}
				value = args[i]
			}
			if value == "" || strings.ContainsAny(value, "\x00\r\n") {
				return "", nil, fmt.Errorf("invalid working directory")
			}
			if filepath.IsAbs(value) {
				directory = filepath.Clean(value)
			} else {
				directory = filepath.Join(cwd, value)
			}
			continue
		}
		result = append(result, arg)
		if agent == "codex" && codexValueOption(arg) {
			i++
			if i >= len(args) {
				return "", nil, fmt.Errorf("value missing for %s", arg)
			}
			result = append(result, args[i])
		}
	}
	return directory, result, nil
}

// Keep option values opaque, even if they resemble a directory flag. This list
// covers scalar options in the supported Codex CLI help; attached values remain
// one original argument. Multi-value image arguments are not rewritten here.
func codexValueOption(value string) bool {
	switch value {
	case "-c", "--config", "--enable", "--disable", "--remote", "--remote-auth-token-env", "-m", "--model", "--local-provider", "-p", "--profile", "-s", "--sandbox", "-a", "--ask-for-approval":
		return true
	}
	return false
}
