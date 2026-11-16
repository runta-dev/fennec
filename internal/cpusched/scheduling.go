// Package cpusched protects the terminal control plane from agent CPU load.
package cpusched

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Command puts the workload boundary inside Herdr's child, not around Herdr.
// Test runtimes and older installations without a resident binary retain their
// original command. The production resident is installed before any launch.
func Command(home string, command []string) []string {
	binary := filepath.Join(home, ".local/fennec/bin/fennec-terminal")
	if info, err := os.Stat(binary); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
		return append([]string{binary, "workload-exec"}, command...)
	}
	return command
}

// CommandFor is Command for an agent of session id: it starts in the
// session's own group (see Focus).
func CommandFor(home, id string, command []string) []string {
	wrapped := Command(home, command)
	if len(wrapped) > len(command) {
		return append([]string{wrapped[0], wrapped[1], "--session", id}, command...)
	}
	return command
}

type Report struct {
	ControlGroup      string   `json:"control_group,omitempty"`
	WorkloadGroup     string   `json:"workload_group,omitempty"`
	ControlProcesses  int      `json:"control_processes"`
	WorkloadProcesses int      `json:"workload_processes"`
	SessionGroups     int      `json:"session_groups"`
	Errors            []string `json:"errors,omitempty"`
}

// focusHold: a session stays weighted up this long after its last input.
const focusHold = 10 * time.Second

var focus struct {
	mu    sync.Mutex
	until map[string]time.Time
}

// Focus weights session id's group above the other sessions' while the user
// types or scrolls in it, until focusHold after the last call. Called for
// every input, it writes the weight only when focus begins and ends.
func Focus(id string) {
	focus.mu.Lock()
	defer focus.mu.Unlock()
	if focus.until == nil {
		focus.until = map[string]time.Time{}
	}
	now := time.Now()
	held := focus.until[id].After(now)
	focus.until[id] = now.Add(focusHold)
	if held {
		return
	}
	if !setFocus(id, true) {
		delete(focus.until, id)
		return
	}
	var check func()
	check = func() {
		focus.mu.Lock()
		defer focus.mu.Unlock()
		if left := time.Until(focus.until[id]); left > 0 {
			time.AfterFunc(left, check)
			return
		}
		delete(focus.until, id)
		setFocus(id, false)
	}
	time.AfterFunc(focusHold, check)
}

// KeepPruned removes the groups of sessions whose processes have all exited,
// every interval until ctx ends. Without it a destroyed session's group
// would stay until the next resident start.
func KeepPruned(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}
