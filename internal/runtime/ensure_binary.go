package runtime

import (
	"context"
	"fennec/internal/setup"
	"os"
	"os/exec"
	"path/filepath"
)

func ensureLocalAgentBinary(ctx context.Context, home, agentID string) error {
	// Only run live on-demand installation inside an initialized Fennec cloud
	// runtime (where fennec-terminal itself is installed), never in unit-test mocks.
	if home == "" {
		return os.ErrNotExist
	}
	if _, err := os.Stat(filepath.Join(home, ".local/fennec/bin/fennec-terminal")); err != nil {
		return err
	}
	script, err := setup.EnsureAgentScript(setup.Agent{ID: agentID, Version: "latest"})
	if err != nil {
		return err
	}
	fullScript := setup.NodeSetupScript + "\n" + script
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", fullScript)
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+filepath.Join(home, ".local/fennec/node/bin")+":"+filepath.Join(home, ".local/fennec/agents/bin")+":"+filepath.Join(home, ".local/bin")+":"+os.Getenv("PATH"))
	return cmd.Run()
}
