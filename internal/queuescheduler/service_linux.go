//go:build linux

package queuescheduler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// systemdRoot is where systemd marks a system it booted (sd_booted(3)).
// Self-hosted Docker computers have no systemd and no such directory.
var systemdRoot = "/run/systemd/system"

// Systemd reports whether systemd manages this computer. Without it,
// EnsureService starts the scheduler as a detached process instead.
func Systemd() bool {
	info, err := os.Stat(systemdRoot)
	return err == nil && info.IsDir()
}

// EnsureService owns only the queue scheduler. Agent sessions remain owned by
// herdr-core and survive a scheduler restart or a lost Mac transport.
func EnsureService(ctx context.Context, home string) error {
	binary, err := filepath.EvalSymlinks(filepath.Join(home, ".local/fennec/bin/fennec-terminal"))
	if err != nil {
		return err
	}
	if !Systemd() {
		return ensureProcess(home, binary)
	}
	return ensureUnit(ctx, home, binary)
}

func ensureUnit(ctx context.Context, home, binary string) error {
	name := "fennec-queues.service"
	dir := "/etc/systemd/system"
	args := []string{}
	if os.Geteuid() != 0 {
		dir = filepath.Join(home, ".config/systemd/user")
		args = append(args, "--user")
	}
	unit := queueServiceUnit(home, binary)
	path := filepath.Join(dir, name)
	old, _ := os.ReadFile(path)
	changed := !bytes.Equal(old, unit)
	run := func(command ...string) error {
		_, err := exec.CommandContext(ctx, "systemctl", append(args, command...)...).Output()
		return err
	}
	if changed {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, unit, 0600); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
		if err := run("daemon-reload"); err != nil {
			return err
		}
		if err := run("enable", name); err != nil {
			return err
		}
		return run("restart", name)
	}
	if run("is-active", "--quiet", name) == nil {
		return nil
	}
	return run("start", name)
}

func serviceDirectory(home string) string {
	return filepath.Join(home, ".local/fennec/queue-service")
}

func lockService(home string) (int, error) {
	dir := serviceDirectory(home)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return -1, err
	}
	fd, err := unix.Open(filepath.Join(dir, "run.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return -1, err
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// ErrServiceRunning means another scheduler already holds the run lock.
var ErrServiceRunning = errors.New("queue scheduler is already running")

// LockService makes "queue-service run" single-instance on a computer, whether
// systemd or EnsureService started it. The lock lasts until release is called
// or the process exits.
func LockService(home string) (release func(), err error) {
	fd, err := lockService(home)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return nil, ErrServiceRunning
	}
	if err != nil {
		return nil, err
	}
	return func() { unix.Close(fd) }, nil
}

// ensureProcess starts "queue-service run" in its own session when no
// scheduler holds the run lock. It is cheap enough to call on every board
// connection, which also restarts a scheduler that exited.
func ensureProcess(home, binary string) error {
	fd, err := lockService(home)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return err
	}
	// Release before starting so the child can take the lock. If two callers
	// race here, the second scheduler finds the lock held and exits.
	unix.Close(fd)
	command := exec.Command(binary, "queue-service", "run")
	command.Dir = home
	command.Env = append(os.Environ(), "HOME="+home, "PATH="+servicePath(home))
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Do not inherit stdio or SSH channels; the log keeps scheduler errors.
	log, err := os.OpenFile(filepath.Join(serviceDirectory(home), "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	command.Stdout = log
	command.Stderr = log
	if err = command.Start(); err != nil {
		return err
	}
	go command.Wait()
	return nil
}

func servicePath(home string) string {
	return strings.Join([]string{
		filepath.Join(home, ".local/fennec/node/bin"),
		filepath.Join(home, ".local/fennec/agents/bin"),
		filepath.Join(home, ".local/bin"),
		"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin",
	}, ":")
}

func queueServiceUnit(home, binary string) []byte {
	path := servicePath(home)
	return []byte(fmt.Sprintf("[Unit]\nDescription=Fennec queue scheduler\nAfter=network.target\n[Service]\nType=simple\nKillMode=process\nEnvironment=HOME=%s\nEnvironment=%s\nExecStart=%s queue-service run\nRestart=on-failure\nRestartSec=3\n[Install]\nWantedBy=default.target\n", strconv.Quote(home), strconv.Quote("PATH="+path), strconv.Quote(binary)))
}
