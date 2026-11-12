package localssh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"fennec/internal/sandbox"
)

// The cloud computer's `fennec-escalate` command. Agents run it where they
// work; it forwards the escalation request to the paired Mac over the device
// alias, where the connector reviews it (user or Codex auto-review). It never
// grants anything itself.

const escalateUsage = `usage: fennec-escalate [--host <mac>] --reason "<why this is needed>" -- '<command>'

Requests approval to run <command> on the paired Mac outside the workspace
sandbox. Quote the command so it runs unchanged on the Mac. --host defaults to
the default paired Mac ("machine").
`

var escalateHostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

// CloudEscalateRemote parses the cloud command line and returns the SSH host
// and the remote command that carries the escalation prefix to the Mac.
func CloudEscalateRemote(args []string) (host, remote string, err error) {
	host = "machine"
	reason := ""
	for len(args) > 0 {
		arg := args[0]
		switch {
		case arg == "--":
			command := strings.TrimSpace(strings.Join(args[1:], " "))
			if len(args) == 2 {
				command = args[1]
			}
			reason = strings.Join(strings.Fields(reason), " ")
			if command == "" || reason == "" || len(reason) > 512 {
				return "", "", fmt.Errorf("a reason and a command are required")
			}
			return host, sandbox.EscalateCommand + " --reason " + sandbox.ShellQuote(reason) + " -- " + sandbox.ShellQuote(command), nil
		case arg == "--reason" && len(args) > 1:
			reason, args = args[1], args[2:]
			continue
		case strings.HasPrefix(arg, "--reason="):
			reason = strings.TrimPrefix(arg, "--reason=")
		case arg == "--host" && len(args) > 1:
			if !escalateHostPattern.MatchString(args[1]) {
				return "", "", fmt.Errorf("invalid host")
			}
			host, args = args[1], args[2:]
			continue
		default:
			return "", "", fmt.Errorf("unexpected argument %q", arg)
		}
		args = args[1:]
	}
	return "", "", fmt.Errorf("missing -- before the command")
}

// RunCloudEscalate replaces this process with ssh so stdio and the exit code
// of the approved command pass through unchanged.
func RunCloudEscalate(args []string) (int, error) {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(escalateUsage)
		return 0, nil
	}
	host, remote, err := CloudEscalateRemote(args)
	if err != nil {
		fmt.Fprint(os.Stderr, escalateUsage)
		return 2, err
	}
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		return 127, fmt.Errorf("ssh unavailable")
	}
	return 1, syscall.Exec(ssh, []string{"ssh", host, remote}, os.Environ())
}

// InstallEscalateEntry links ~/.local/bin/fennec-escalate to the stable
// terminal service path. An unrelated existing file is never replaced.
func InstallEscalateEntry(home string) error {
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	target := filepath.Join(home, ".local", "fennec", "bin", "fennec-terminal")
	entry := filepath.Join(dir, sandbox.EscalateCommand)
	if info, err := os.Lstat(entry); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		if current, _ := os.Readlink(entry); current == target {
			return nil
		}
	}
	tmp := entry + ".tmp"
	os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, entry)
}
