//go:build linux

package queuescheduler

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// EnsureService owns only the queue scheduler. Agent sessions remain owned by
// herdr-core and survive a scheduler restart or a lost Mac transport.
func EnsureService(ctx context.Context, home string) error {
	name := "fennec-queues.service"
	dir := "/etc/systemd/system"
	args := []string{}
	if os.Geteuid() != 0 {
		dir = filepath.Join(home, ".config/systemd/user")
		args = append(args, "--user")
	}
	binary, err := filepath.EvalSymlinks(filepath.Join(home, ".local/fennec/bin/fennec-terminal"))
	if err != nil {
		return err
	}
	unit := []byte(fmt.Sprintf("[Unit]\nDescription=Fennec queue scheduler\nAfter=network.target\n[Service]\nType=simple\nKillMode=process\nEnvironment=HOME=%s\nExecStart=%s queue-service run\nRestart=on-failure\nRestartSec=3\n[Install]\nWantedBy=default.target\n", strconv.Quote(home), strconv.Quote(binary)))
	path := filepath.Join(dir, name)
	old, _ := os.ReadFile(path)
	changed := !bytes.Equal(old, unit)
	run := func(command ...string) error {
		_, err := exec.CommandContext(ctx, "systemctl", append(args, command...)...).Output()
		return err
	}
	if changed {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, unit, 0600); err != nil {
			return err
		}
		if err = os.Rename(tmp, path); err != nil {
			return err
		}
		if err = run("daemon-reload"); err != nil {
			return err
		}
		if err = run("enable", name); err != nil {
			return err
		}
		return run("restart", name)
	}
	if run("is-active", "--quiet", name) == nil {
		return nil
	}
	return run("start", name)
}
