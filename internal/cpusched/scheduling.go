// Package cpusched protects the terminal control plane from agent CPU load.
package cpusched

import (
	"os"
	"path/filepath"
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

type Report struct {
	ControlGroup      string   `json:"control_group,omitempty"`
	WorkloadGroup     string   `json:"workload_group,omitempty"`
	ControlProcesses  int      `json:"control_processes"`
	WorkloadProcesses int      `json:"workload_processes"`
	Errors            []string `json:"errors,omitempty"`
}
