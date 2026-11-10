package launcher

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
)

// AttachArgs delegates terminal allocation, resize and signals to OpenSSH and
// Herdr. Only a previously provisioned Fennec key and pinned host file are used.
func AttachArgs(runtimeID, address, sessionID, directory string) ([]string, error) {
	if !regexp.MustCompile(`^[a-f0-9-]{36}$`).MatchString(runtimeID) || !regexp.MustCompile(`^t[0-9a-z]+$`).MatchString(address) || !regexp.MustCompile(`^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`).MatchString(sessionID) {
		return nil, fmt.Errorf("invalid runtime or session identity")
	}
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("absolute SSH identity directory required")
	}
	remote := "\"$HOME/.local/fennec/bin/fennec-terminal\" attach " + address + " " + sessionID
	control, err := SessionControlPath(directory, runtimeID, sessionID)
	if err != nil {
		return nil, err
	}
	return []string{"ssh", "-tt", "-S", control, "-o", "ControlMaster=no", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=8", "-o", "UserKnownHostsFile=" + filepath.Join(directory, "known_hosts"), "-i", filepath.Join(directory, "runtime_key"), runtimeID + "@ssh.runta.com", remote}, nil
}
func Attach(runtimeID, address, sessionID, directory string) error {
	_, err := AttachArgs(runtimeID, address, sessionID, directory)
	if err != nil {
		return err
	}
	for _, name := range []string{"runtime_key", "known_hosts"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("verified Fennec SSH credentials are not ready")
		}
	}
	return AttachWithLoading(runtimeID, address, sessionID, directory, func() {})
}

type terminalOutput struct {
	writer io.Writer
	ready  func()
}

func (output terminalOutput) Write(data []byte) (int, error) {
	if len(data) > 0 {
		output.ready()
	}
	return output.writer.Write(data)
}

func AttachWithLoading(runtimeID, address, sessionID, directory string, ready func()) error {
	args, err := AttachArgs(runtimeID, address, sessionID, directory)
	if err != nil {
		return err
	}
	var once sync.Once
	clear := func() { once.Do(ready) }
	defer clear()
	command := exec.Command("/usr/bin/ssh", args[1:]...)
	command.Stdin = os.Stdin
	titles := &SessionTitleWriter{Writer: os.Stdout, SessionID: sessionID}
	defer titles.Flush()
	command.Stdout = terminalOutput{titles, clear}
	diagnostics := &sshDiagnostics{writer: terminalOutput{os.Stderr, clear}}
	defer diagnostics.Flush()
	command.Stderr = diagnostics
	err = command.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		code := exit.ExitCode()
		if code < 0 {
			code = 1
		}
		os.Exit(code)
	}
	return err
}
