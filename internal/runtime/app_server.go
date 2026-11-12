package runtime

import (
	"context"
	"errors"
	"fennec/internal/setup"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Codex app-server modes follow the cloud Codex CLI: "gateway" uses the Magpie
// routed ~/.codex configuration, "native" the synchronized native account
// environment. Each mode has its own CODEX_HOME and therefore its own server.
const (
	CodexAppServerGateway = "gateway"
	CodexAppServerNative  = "native"
)

// CodexAppServerSocket is the control socket that `codex app-server --listen
// unix://` and `codex app-server proxy` agree on for a CODEX_HOME.
func CodexAppServerSocket(codexHome string) string {
	return filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
}

// codexAppServerEnvironment mirrors how the cloud launches Codex CLI in the
// current agent mode, so desktop and terminal sessions share configuration.
func codexAppServerEnvironment(home string) (mode string, env []string, codexHome string, err error) {
	if setup.NativeAgentMode(home) {
		env, err = setup.NativeAgentEnvironment(home, "codex")
		if err != nil {
			return "", nil, "", err
		}
		for _, entry := range env {
			if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
				codexHome = value
			}
		}
		if codexHome == "" {
			return "", nil, "", fmt.Errorf("native codex home unavailable")
		}
		return CodexAppServerNative, env, codexHome, nil
	}
	codexHome = os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	return CodexAppServerGateway, os.Environ(), codexHome, nil
}

// appServerConn reports which mode served the stream.
type appServerConn struct {
	net.Conn
	mode string
}

func (conn appServerConn) StreamLabel() string { return conn.mode }

// DialCodexAppServer connects to the runtime owner's codex app-server. A running
// server (including one managed by `codex app-server daemon`) is reused; only a
// missing socket starts the already-installed codex, detached from the caller so
// desktop threads survive terminal-channel reconnects. No version is compared
// and nothing is installed here.
func DialCodexAppServer(ctx context.Context, home string) (net.Conn, error) {
	mode, env, codexHome, err := codexAppServerEnvironment(home)
	if err != nil {
		return nil, err
	}
	socket := CodexAppServerSocket(codexHome)
	conn, err := dialCodexAppServer(ctx, home, socket, env)
	if err != nil {
		return nil, err
	}
	return appServerConn{Conn: conn, mode: mode}, nil
}

func dialCodexAppServer(ctx context.Context, home, socket string, env []string) (net.Conn, error) {
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
	command.Env = env
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
