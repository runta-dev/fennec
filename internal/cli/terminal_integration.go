package cli

import (
	"context"
	"fennec/internal/agent"
	"fennec/internal/agentcmd"
	"fennec/internal/launcher"
	"fennec/internal/setup"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// terminalIntegrationReport is what the last reconcile did, for diagnosis
// (fen doctor agents) and the Agents tab. It names files and reasons only.
type terminalIntegrationReport struct {
	At         time.Time            `json:"at"`
	Dispatcher string               `json:"dispatcher"`
	Stable     string               `json:"stable_error,omitempty"`
	Shims      string               `json:"shims_error,omitempty"`
	Shell      launcher.ShellReport `json:"shell"`
	Entries    map[string]string    `json:"entries,omitempty"`
}

// launcherDispatcher is what launchers should call: the stable dispatcher once
// it is registered for this executable's application, else the executable.
func launcherDispatcher(home, executable string) string {
	stable := launcher.StableExecutable(home)
	if info, err := os.Stat(stable); err == nil && info.Mode().IsRegular() && launcher.RegisteredExecutable(home) == executable {
		return stable
	}
	return executable
}

func terminalIntegrationPath(home string) string {
	return filepath.Join(home, ".config/fennec/diagnostics/terminal-integration.json")
}

// terminalIntegrationWanted reports whether the user chose the cloud computer
// for an agent before or has Fennec's shell integration already.
func terminalIntegrationWanted(home string) bool {
	if enabled, err := launcher.EnabledAgents(filepath.Join(home, ".config/fennec/takeover.json")); err == nil && len(enabled) > 0 {
		return true
	}
	if len(launcher.RecordedShellFiles(home)) > 0 {
		return true
	}
	if entries, err := os.ReadDir(filepath.Join(home, ".local/share/fennec/shims")); err == nil && len(entries) > 0 {
		return true
	}
	return false
}

// loginShell is the account's login shell, not the daemon's SHELL: a GUI
// LaunchAgent's environment does not carry the user's shell.
func loginShell(ctx context.Context) string {
	if runtime.GOOS == "darwin" {
		if u, err := user.Current(); err == nil {
			step, cancel := context.WithTimeout(ctx, 5*time.Second)
			out, err := exec.CommandContext(step, "/usr/bin/dscl", ".", "-read", "/Users/"+u.Username, "UserShell").Output()
			cancel()
			if err == nil {
				if _, shell, ok := strings.Cut(strings.TrimSpace(string(out)), "UserShell:"); ok && filepath.IsAbs(strings.TrimSpace(shell)) {
					return strings.TrimSpace(shell)
				}
			}
		}
	}
	if shell := os.Getenv("SHELL"); filepath.IsAbs(shell) {
		return shell
	}
	if runtime.GOOS == "darwin" {
		return "/bin/zsh"
	}
	return "/bin/bash"
}

var terminalReconcile struct {
	sync.Mutex
	running, again bool
	selection      string
}

// reconcileTerminalIntegrationOnSelection reconciles when the selected agents
// change; selection events repeat with every runtime event.
func reconcileTerminalIntegrationOnSelection(home, executable, selection string) {
	terminalReconcile.Lock()
	changed := terminalReconcile.selection != selection
	terminalReconcile.selection = selection
	terminalReconcile.Unlock()
	if changed {
		reconcileTerminalIntegrationSoon(home, executable)
	}
}

// reconcileTerminalIntegrationSoon runs one reconcile in the background,
// coalescing requests made while one runs.
func reconcileTerminalIntegrationSoon(home, executable string) {
	terminalReconcile.Lock()
	if terminalReconcile.running {
		terminalReconcile.again = true
		terminalReconcile.Unlock()
		return
	}
	terminalReconcile.running = true
	terminalReconcile.Unlock()
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			reconcileTerminalIntegration(ctx, home, executable)
			cancel()
			terminalReconcile.Lock()
			if !terminalReconcile.again {
				terminalReconcile.running = false
				terminalReconcile.Unlock()
				return
			}
			terminalReconcile.again = false
			terminalReconcile.Unlock()
		}
	}()
}

// reconcileTerminalIntegration installs the stable dispatcher, the cleanup
// agent, the shims, the user's shell integration and the selected agents'
// entry wrappers. Nothing here fails the cloud handoff: every failure is
// recorded in the report instead.
func reconcileTerminalIntegration(ctx context.Context, home, executable string) terminalIntegrationReport {
	report := terminalIntegrationReport{At: time.Now().UTC(), Dispatcher: executable, Entries: map[string]string{}}
	if app := installedBundleForTrashWatch(executable, home); app != "" {
		stable, err := launcher.RefreshStable(home, filepath.Join(app, "Contents/MacOS/fen"))
		if err != nil {
			report.Stable = err.Error()
		} else {
			report.Dispatcher = stable
			if err := ensureCleanupAgent(ctx, home, stable, app); err != nil {
				report.Stable = "cleanup agent: " + err.Error()
			}
		}
	}
	if err := launcher.InstallShims(filepath.Join(home, ".local/share/fennec/shims"), report.Dispatcher, agent.ExecutableSearchPath(home)); err != nil {
		report.Shims = err.Error()
	}
	zdot, xdg := setup.LocalShellDirectories(ctx)
	report.Shell = launcher.InstallShellTargets(launcher.ShellTargets{Home: home, Shell: loginShell(ctx), ZDotDir: zdot, XDGConfig: xdg, Dispatcher: report.Dispatcher})
	enabled, _ := launcher.EnabledAgents(filepath.Join(home, ".config/fennec/takeover.json"))
	for id := range enabled {
		entry, err := agent.EntryPath(id)
		if err != nil {
			report.Entries[id] = "no_entry"
			continue
		}
		if err := launcher.ReconcileEntry(entry, report.Dispatcher, id); err != nil {
			report.Entries[id] = err.Error()
			continue
		}
		report.Entries[id] = "wrapped " + entry
	}
	for _, entry := range agent.ManagedEntryPaths(home) {
		for id := range launcher.Supported {
			if filepath.Base(entry) == agentcmd.BinaryName(id) {
				_ = launcher.ReconcileEntry(entry, report.Dispatcher, id)
			}
		}
	}
	_ = writePublicJSON(terminalIntegrationPath(home), report)
	return report
}
