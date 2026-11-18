package cli

import (
	"errors"
	"fennec/internal/agentcmd"
	"fennec/internal/launcher"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// agentForCommand maps a command name (cursor-agent) to its agent id (cursor).
func agentForCommand(name string) string {
	for id := range launcher.Supported {
		if agentcmd.BinaryName(id) == name || id == name {
			return id
		}
	}
	return ""
}

// runningAsStable reports whether this process is the stable dispatcher copy.
func runningAsStable(home string) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	a, errA := os.Stat(self)
	b, errB := os.Stat(launcher.StableExecutable(home))
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// dispatchStable is the stable dispatcher's whole decision. It hands every
// dispatch to the installed application's fen, so the copy never needs the
// application's newer behavior, and runs the agent on this Mac when the
// application is gone, when the cloud computer started this command through
// the relay, or when an agent this Mac already runs starts it. entry is the
// wrapped original for an entry wrapper, or "".
func dispatchStable(home, agentID, entry string, args []string) error {
	if !launcher.Supported[agentID] {
		return errors.New("supported agent required")
	}
	local := func() error {
		if entry != "" {
			return syscall.Exec(entry, append([]string{entry}, args...), launcher.LocalEnvironment())
		}
		return launcher.ExecLocal(agentID, args, filepath.Join(home, ".local/share/fennec/shims"))
	}
	if launcher.RunLocally() {
		return local()
	}
	if !launcher.ApplicationPresent(home) {
		if launcher.RegisteredExecutable(home) != "" {
			fmt.Fprintln(os.Stderr, "Fennec is no longer installed here. Running locally.")
		}
		return local()
	}
	app := launcher.RegisteredExecutable(home)
	argv := []string{app, "dispatch-agent", agentID}
	if entry != "" {
		argv = []string{app, "dispatch-entry", agentID, entry}
	}
	return syscall.Exec(app, append(argv, args...), os.Environ())
}
