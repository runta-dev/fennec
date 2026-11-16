//go:build !linux

package cpusched

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func Interactive(home string) Report {
	return Report{Errors: []string{"CPU groups require Linux cgroup v2"}}
}
func ExecWorkload(command []string) error {
	if len(command) >= 2 && command[0] == "--session" {
		command = command[2:]
	}
	if len(command) == 0 {
		return fmt.Errorf("workload command required")
	}
	path, err := exec.LookPath(command[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, command, os.Environ())
}

func setFocus(id string, on bool) bool { return false }

func prune() {}
