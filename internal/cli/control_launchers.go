package cli

import (
	"errors"
	"fennec/internal/agent"
	"fennec/internal/agentcmd"
	"fennec/internal/installation"
	"fennec/internal/launcher"
	"fmt"
	"path/filepath"
	"runtime"
)

// Refresh wrappers as soon as the installed daemon owns its socket. Upgrades
// must not depend on restoring credentials or reconnecting the cloud broker.
// Standalone development tools must never redirect the user's installed shims.
func refreshInstalledLaunchers(home, executable string) error {
	if runtime.GOOS != "darwin" || filepath.Base(filepath.Dir(executable)) != "MacOS" {
		return nil
	}
	location, err := installation.Inspect(executable, home)
	if err != nil {
		return err
	}
	if location.Required {
		return fmt.Errorf("launcher refresh requires an installed application")
	}
	current := filepath.Join(location.Application, "Contents/MacOS/fen")
	return refreshManagedLaunchers(home, launcherDispatcher(home, current), agent.ExecutableSearchPath(home), agent.ManagedEntryPaths(home))
}

func refreshManagedLaunchers(home, executable, searchPath string, entries []string) error {
	failures := []error{}
	if err := launcher.InstallShims(filepath.Join(home, ".local/share/fennec/shims"), executable, searchPath); err != nil {
		failures = append(failures, err)
	}
	for _, entry := range entries {
		for id := range launcher.Supported {
			if filepath.Base(entry) != agentcmd.BinaryName(id) {
				continue
			}
			if err := launcher.ReconcileEntry(entry, executable, id); err != nil {
				failures = append(failures, fmt.Errorf("%s entry: %w", id, err))
			}
			break
		}
	}
	return errors.Join(failures...)
}
