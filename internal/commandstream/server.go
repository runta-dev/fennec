package commandstream

import (
	"context"
	"encoding/binary"
	"errors"
	"fennec/internal/cpusched"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Dial serves one command as the already authenticated resident owner. Closing
// the returned stream cancels its whole process group, even if stdin is blocked.
func Dial(parent context.Context) net.Conn {
	local, remote := net.Pipe()
	ctx, cancel := context.WithCancel(parent)
	go func() { defer cancel(); defer remote.Close(); serve(ctx, remote) }()
	return &ownedConn{Conn: local, cancel: cancel}
}

type ownedConn struct {
	net.Conn
	cancel context.CancelFunc
}

func (c *ownedConn) Close() error { c.cancel(); return c.Conn.Close() }
func serve(parent context.Context, conn net.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	// A peer that never sends its command cannot retain a stream indefinitely.
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	kind, data, err := readRecord(conn)
	if err != nil || kind != request || len(data) == 0 || strings.ContainsRune(string(data), 0) {
		return
	}
	conn.SetReadDeadline(time.Time{})
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	home, _ := os.UserHomeDir()
	args := cpusched.Command(home, []string{shell, "-c", string(data)})
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = home
	// Resident control priority must not be inherited by arbitrary bulk commands.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	defer stdin.Close()
	wire := &records{out: conn}
	cmd.Stdout = outputWriter{wire, stdoutData}
	cmd.Stderr = outputWriter{wire, stderrData}
	if err = cmd.Start(); err != nil {
		wire.send(exitStatus, status(127))
		return
	}
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		defer stdin.Close()
		eof := false
		for {
			kind, data, err := readRecord(conn)
			if err != nil {
				cancel()
				return
			}
			switch {
			case kind == stdinData && !eof && len(data) <= chunk:
				if err = writeAll(stdin, data); err != nil {
					// Programs may intentionally close stdin early. Drain bounded records so
					// their exit status still reaches the caller without aborting the command.
					stdin.Close()
				}
			case kind == stdinEOF && len(data) == 0 && !eof:
				eof = true
				stdin.Close()
			default:
				cancel()
				return
			}
		}
	}()
	err = cmd.Wait()
	code := 0
	if err != nil {
		code = 255
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
			if code < 0 {
				code = 128
			}
		}
	}
	wire.send(exitStatus, status(code))
	cancel()
	conn.Close()
	<-inputDone
}
func status(code int) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(code))
	return b[:]
}

// ExitError retains the remote exit status without exposing command text.
type ExitError struct{ Code int }

func (e *ExitError) Error() string {
	return fmt.Sprintf("remote command exited with status %d", e.Code)
}
func (e *ExitError) ExitStatus() int { return e.Code }
