package cli

import (
	"os"
	"path/filepath"
	"time"
)

func connectionWakePath(home string) string {
	return filepath.Join(home, ".config/fennec/ipc/connection-ready.json")
}

// This is a wake hint, never evidence of authorization or transport readiness.
// Clients recheck the live control socket after a kernel file-change event.
// Atomic replacement also wakes clients that were watching the previous daemon.
func notifyConnectionReady(home string) {
	if home == "" {
		return
	}
	_ = writePublicJSON(connectionWakePath(home), map[string]any{"pid": os.Getpid(), "changed_ns": time.Now().UnixNano()})
}
