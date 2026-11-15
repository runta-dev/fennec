package setup

import (
	"context"
	"encoding/hex"
	"fennec/internal/agentcmd"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// PreparationHealth identifies a fixed, read-only check, never a caller-supplied
// command. The same checks serve the SSH bootstrap and the resident channel.
type PreparationHealth struct {
	Fingerprint string   `json:"fingerprint"`
	Marker      string   `json:"marker"`
	Agents      []string `json:"agents,omitempty"`
	Magpie      bool     `json:"magpie"`
}

func (h PreparationHealth) Validate() error {
	if h.Marker != "prepared-runtime" && h.Marker != "prepared-installation" {
		return fmt.Errorf("invalid preparation marker")
	}
	if len(h.Fingerprint) != 64 {
		return fmt.Errorf("invalid preparation fingerprint")
	}
	if _, err := hex.DecodeString(h.Fingerprint); err != nil {
		return fmt.Errorf("invalid preparation fingerprint")
	}
	for _, id := range h.Agents {
		switch id {
		case "claude", "codex", "pi", "opencode", "kimi", "cursor", "grok":
		default:
			return fmt.Errorf("invalid preparation agent")
		}
	}
	return nil
}

func (h PreparationHealth) Script() string {
	if h.Validate() != nil {
		return "exit 1"
	}
	script := `set -eu
export PATH="$HOME/.local/fennec/node/bin:$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH"
test "$(cat "$HOME/.config/fennec/` + h.Marker + `")" = '` + h.Fingerprint + `'
command -v node >/dev/null
test -x "$HOME/.local/fennec/bin/fennec-terminal"
herdr-core server status >/dev/null 2>&1
`
	if h.Magpie {
		script += "command -v magpie >/dev/null\ncurl --fail --silent --max-time 2 http://127.0.0.1:3425/v1/models >/dev/null\n"
	}
	for _, id := range h.Agents {
		script += "command -v " + agentcmd.BinaryName(id) + " >/dev/null\n"
	}
	return script
}

func CheckPreparationHealth(ctx context.Context, home string, health PreparationHealth) error {
	if err := health.Validate(); err != nil {
		return err
	}
	// Bound the remote worker too: the client abandoning an operation does not
	// cancel the resident service's per-request context on older protocols.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", "-c", health.Script())
	command.Env = append(os.Environ(), "HOME="+home)
	if err := command.Run(); err != nil {
		return fmt.Errorf("prepared runtime health check failed: %w", err)
	}
	return nil
}
