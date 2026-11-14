package setup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RefreshMagpieProcess lets the existing supervisor start an upgraded binary.
// Only its own child with the old executable inode receives SIGTERM; unchanged
// gateways and other Magpie processes are left running. Magpie drains requests.
func RefreshMagpieProcess(ctx context.Context, home string) error {
	data, err := os.ReadFile(filepath.Join(home, ".config/fennec/magpie-supervisor.pid"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	parent, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || parent <= 1 {
		return fmt.Errorf("invalid Magpie supervisor identity")
	}
	parentRoot := filepath.Join("/proc", strconv.Itoa(parent))
	command, err := os.ReadFile(filepath.Join(parentRoot, "cmdline"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !hasProcessArgument(command, filepath.Join(home, ".local/fennec/magpie-supervisor.sh")) {
		return nil
	}
	children, err := os.ReadFile(filepath.Join(parentRoot, "task", strconv.Itoa(parent), "children"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	binary := filepath.Join(home, ".local/bin/magpie")
	installed, err := os.Stat(binary)
	if err != nil {
		return err
	}
	for _, value := range strings.Fields(string(children)) {
		pid, err := strconv.Atoi(value)
		if err != nil || pid <= 1 {
			continue
		}
		root := filepath.Join("/proc", value)
		exe := filepath.Join(root, "exe")
		target, err := os.Readlink(exe)
		if err != nil || strings.TrimSuffix(target, " (deleted)") != binary {
			continue
		}
		running, err := os.Stat(exe)
		if err != nil || os.SameFile(installed, running) {
			continue
		}
		args, err := os.ReadFile(filepath.Join(root, "cmdline"))
		if err != nil || !hasProcessArgument(args, "serve") {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
			return err
		}
		wait, cancel := context.WithTimeout(ctx, 45*time.Second)
		ticker := time.NewTicker(50 * time.Millisecond)
		for {
			current, err := os.Stat(exe)
			if os.IsNotExist(err) || err == nil && !os.SameFile(current, running) {
				break
			}
			select {
			case <-wait.Done():
				ticker.Stop()
				cancel()
				return fmt.Errorf("Magpie is still finishing requests; retry preparation shortly")
			case <-ticker.C:
			}
		}
		ticker.Stop()
		cancel()
	}
	return nil
}

func hasProcessArgument(command []byte, argument string) bool {
	for _, field := range bytes.Split(command, []byte{0}) {
		if string(field) == argument {
			return true
		}
	}
	return false
}
