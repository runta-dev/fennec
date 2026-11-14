package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func validateAgentInventory(ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		switch id {
		case "claude", "codex", "pi", "opencode", "kimi":
		default:
			return fmt.Errorf("unsupported agent inventory")
		}
		if seen[id] {
			return fmt.Errorf("duplicate agent inventory")
		}
		seen[id] = true
	}
	return nil
}

func availableAgentInventory(home string, ids []string) map[string]bool {
	result := map[string]bool{}
	for _, id := range ids {
		result[id] = agentBinaryAvailable(home, id, exec.LookPath)
	}
	return result
}

// Managed binaries must retain their working alias. A missing/stale alias falls
// back to the existing installer/repair path. Other executable installations are
// reusable without probing or comparing their versions.
func agentBinaryAvailable(home, id string, lookup func(string) (string, error)) bool {
	managed := filepath.Join(home, ".local/fennec/agents/bin", id)
	if info, err := os.Stat(managed); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
		alias, err := os.Stat(filepath.Join(home, ".local/bin", id))
		return err == nil && os.SameFile(info, alias)
	}
	path, err := lookup(id)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}
