package launcher

import (
	"context"
	"encoding/hex"
	"fennec/internal/launchprofile"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

func AttachShared(runtimeID, address, sessionID, directory, control string) error {
	return AttachSharedWithLoading(context.Background(), runtimeID, address, sessionID, directory, control, func() {})
}

func AttachSharedWithLoading(ctx context.Context, runtimeID, address, sessionID, directory, control string, ready func()) error {
	return attachShared(ctx, runtimeID, address, sessionID, directory, control, ready, true)
}

func AttachExistingShared(ctx context.Context, runtimeID, address, sessionID, directory, control string, ready func()) error {
	return attachShared(ctx, runtimeID, address, sessionID, directory, control, ready, false)
}

func attachShared(ctx context.Context, runtimeID, address, sessionID, directory, control string, ready func(), restoreHistory bool) error {
	args, err := AttachArgs(runtimeID, address, sessionID, directory)
	if err != nil {
		return err
	}
	name := filepath.Base(control)
	if _, err := hex.DecodeString(name); err != nil || len(name) != 24 || filepath.Dir(control) != filepath.Join(directory, "mux") || filepath.Clean(control) != control {
		return fmt.Errorf("invalid shared SSH connection")
	}
	info, err := os.Lstat(filepath.Dir(control))
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("shared SSH directory must be private")
	}
	args[3] = control
	if restoreHistory {
		args[len(args)-1] = resumeHistoryCommand(address, sessionID)
	}
	remote := append([]string(nil), args[len(args)-2:]...)
	args = append(args[:len(args)-2], "-o", "ProxyCommand=false", "-o", "LogLevel=ERROR")
	args = append(args, remote...)
	var once sync.Once
	firstOutput := launchprofile.Begin("resume.ssh_to_first_output")
	clear := func() { once.Do(func() { firstOutput(); ready() }) }
	defer clear()
	titles := &SessionTitleWriter{Writer: os.Stdout, SessionID: sessionID}
	defer titles.Flush()
	command := exec.CommandContext(ctx, "/usr/bin/ssh", args[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = terminalOutput{titles, clear}
	diagnostics := &sshDiagnostics{sessionID: sessionID, writer: terminalOutput{os.Stderr, clear}}
	defer diagnostics.Flush()
	command.Stderr = diagnostics
	err = command.Run()
	diagnostics.Finish(err)
	if status, ok := err.(*exec.ExitError); ok {
		return RemoteExitError{Code: max(1, status.ExitCode())}
	}
	return err
}

func AttachNative(ctx context.Context, receipt LaunchReceipt, directory string, ready func()) error {
	args, err := AttachArgs(receipt.RuntimeID, receipt.Address, receipt.SessionID, directory)
	if err != nil {
		return err
	}
	args[len(args)-1] = `"$HOME/.local/fennec/bin/fennec-terminal" attach ` + receipt.Address + " " + receipt.SessionID
	var once sync.Once
	clear := func() { once.Do(ready) }
	defer clear()
	titles := &SessionTitleWriter{Writer: os.Stdout, SessionID: receipt.SessionID}
	defer titles.Flush()
	command := exec.CommandContext(ctx, "/usr/bin/ssh", args[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = terminalOutput{titles, clear}
	diagnostics := &sshDiagnostics{sessionID: receipt.SessionID, writer: terminalOutput{os.Stderr, clear}}
	defer diagnostics.Flush()
	command.Stderr = diagnostics
	err = command.Run()
	diagnostics.Finish(err)
	if exit, ok := err.(*exec.ExitError); ok {
		return RemoteExitError{Code: max(1, exit.ExitCode())}
	}
	return err
}
