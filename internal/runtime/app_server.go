package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// CodexAppServerSocket is the control socket that `codex app-server --listen
// unix://` and `codex app-server proxy` agree on.
func CodexAppServerSocket(home string) string {
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	return filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
}

// DialCodexAppServer connects to the runtime owner's codex app-server. A running
// server (including one managed by `codex app-server daemon`) is reused; only a
// missing socket starts the already-installed codex, detached from the caller so
// desktop threads survive terminal-channel reconnects. No version is compared
// and nothing is installed here.
func DialCodexAppServer(ctx context.Context, home string) (net.Conn, error) {
	socket := CodexAppServerSocket(home)
	if conn, err := dialUnix(ctx, socket); err == nil {
		return conn, nil
	}
	directory := filepath.Dir(socket)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(directory, "fennec-start.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	for {
		if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			break
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if conn, err := dialUnix(ctx, socket); err == nil {
		return conn, nil
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		return nil, fmt.Errorf("codex is not installed in the runtime")
	}
	logFile, err := os.OpenFile(filepath.Join(directory, "app-server.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	command := exec.Command(codex, "-c", "features.code_mode_host=true", "app-server", "--listen", "unix://")
	command.Dir = home
	command.Stdout, command.Stderr = logFile, logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = command.Start()
	logFile.Close()
	if err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() { command.Wait(); close(exited) }()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		if conn, err := dialUnix(ctx, socket); err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-exited:
			if conn, err := dialUnix(ctx, socket); err == nil {
				return conn, nil
			}
			return nil, fmt.Errorf("codex app-server exited during startup")
		case <-deadline.C:
			return nil, fmt.Errorf("codex app-server did not become ready")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func dialUnix(ctx context.Context, path string) (net.Conn, error) {
	attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var dialer net.Dialer
	return dialer.DialContext(attempt, "unix", path)
}
