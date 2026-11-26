package launcher

import "errors"

// ErrNotInstalled is returned when an agent should run on this Mac but its
// CLI is not here. A config folder kept by a desktop app (~/.codex from Codex
// Desktop) does not make the CLI present (FEN-27).
var ErrNotInstalled = errors.New("agent CLI not installed")

var agentNames = map[string]string{"claude": "Claude Code", "codex": "Codex CLI", "pi": "Pi", "opencode": "OpenCode", "kimi": "Kimi Code", "cursor": "Cursor CLI", "grok": "Grok Build", "agy": "Antigravity CLI"}

// InstallCommand is the vendor's macOS install command for an agent's CLI,
// following Magpie's internal/agent/install.go, or "" when none is known.
func InstallCommand(agent string) string {
	return map[string]string{
		"claude":   "curl -fsSL https://claude.ai/install.sh | bash",
		"codex":    "curl -fsSL https://chatgpt.com/codex/install.sh | sh",
		"opencode": "curl -fsSL https://opencode.ai/v2/install | bash",
		"cursor":   "curl https://cursor.com/install -fsS | bash",
		"pi":       "npm install -g @earendil-works/pi-coding-agent",
		"kimi":     "npm install -g @moonshot-ai/kimi-code",
	}[agent]
}

// notInstalled explains what to do instead of naming the shim directory.
func notInstalled(agent string) error {
	name := agentNames[agent]
	if name == "" {
		name = agent
	}
	message := name + " is not installed on this Mac."
	if command := InstallCommand(agent); command != "" {
		message += "\nInstall it with:\n  " + command
	}
	message += "\nOr run it on the cloud computer: Fennec › Agents › " + name + " › Cloud computer."
	return notInstalledError(message)
}

type notInstalledError string

func (e notInstalledError) Error() string        { return string(e) }
func (e notInstalledError) Is(target error) bool { return target == ErrNotInstalled }
