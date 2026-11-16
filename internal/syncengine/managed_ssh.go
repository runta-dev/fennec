package syncengine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ConfigureManagedSSH uses Mutagen's existing private wrapper boundary, while
// both agent installation and agent communication borrow the daemon resident.
// Identity and host keys remain enforced by that authenticated connection.
func ConfigureManagedSSH(directory, executable, owner, runtimeID string) (string, error) {
	if !filepath.IsAbs(directory) || !filepath.IsAbs(executable) || owner == "" || len(owner) > 256 || strings.ContainsAny(owner+executable, "\x00\r\n") || !regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`).MatchString(runtimeID) {
		return "", fmt.Errorf("invalid managed SSH scope")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("private SSH wrapper directory required")
	}
	ssh := filepath.Join(directory, "ssh")
	scripts := map[string]string{
		"ssh": "#!/bin/sh\nexec " + quoteShell(executable) + " transfer-ssh " + quoteShell(owner) + " " + quoteShell(runtimeID) + " -- \"$@\"\n",
		"scp": "#!/bin/sh\nexec /usr/bin/scp -S " + quoteShell(ssh) + " \"$@\"\n",
	}
	for name, script := range scripts {
		temp, err := os.CreateTemp(directory, ".wrapper-")
		if err != nil {
			return "", err
		}
		path := temp.Name()
		_, writeErr := temp.WriteString(script)
		modeErr := temp.Chmod(0700)
		closeErr := temp.Close()
		if writeErr != nil || modeErr != nil || closeErr != nil {
			os.Remove(path)
			return "", fmt.Errorf("cannot write managed SSH wrapper")
		}
		if err = os.Rename(path, filepath.Join(directory, name)); err != nil {
			os.Remove(path)
			return "", err
		}
	}
	return "fennec-cloud", nil
}

// ManagedSSHCommand accepts the non-PTY subset used by Mutagen and scp. Options
// that could redirect a connection, forward sockets, or weaken authentication
// are rejected. The resident already owns keepalive and cryptographic settings.
func ManagedSSHCommand(args []string, runtimeID string) (string, error) {
	total := 0
	for _, arg := range args {
		total += len(arg)
		if strings.ContainsRune(arg, 0) {
			return "", fmt.Errorf("invalid SSH argument")
		}
	}
	if total > 128<<10 || len(args) > 128 {
		return "", fmt.Errorf("oversized SSH arguments")
	}
	subsystem := false
	index := 0
	for index < len(args) && strings.HasPrefix(args[index], "-") {
		arg := args[index]
		index++
		if arg == "--" {
			break
		}
		switch arg {
		case "-T", "-C", "-q", "-x", "-a":
			continue
		case "-s":
			subsystem = true
			continue
		}
		flag, value := "", ""
		if arg == "-o" || arg == "-p" || arg == "-l" {
			if index == len(args) {
				return "", fmt.Errorf("missing SSH option value")
			}
			flag = arg
			value = args[index]
			index++
		} else if len(arg) > 2 && (arg[:2] == "-o" || arg[:2] == "-p" || arg[:2] == "-l") {
			flag = arg[:2]
			value = arg[2:]
		} else {
			return "", fmt.Errorf("unsupported managed SSH option")
		}
		switch flag {
		case "-p":
			if value != "22" {
				return "", fmt.Errorf("managed SSH port cannot change")
			}
		case "-l":
			if value != runtimeID {
				return "", fmt.Errorf("managed SSH identity cannot change")
			}
		case "-o":
			key, value, ok := strings.Cut(value, "=")
			if !ok {
				return "", fmt.Errorf("invalid managed SSH option")
			}
			key = strings.ToLower(key)
			value = strings.ToLower(value)
			switch key {
			case "connecttimeout", "serveraliveinterval", "serveralivecountmax":
				n, err := strconv.Atoi(value)
				if err != nil || n < 1 || n > 3600 {
					return "", fmt.Errorf("invalid SSH timeout")
				}
			case "batchmode", "clearallforwardings":
				if value != "yes" {
					return "", fmt.Errorf("unsupported SSH option value")
				}
			case "forwardagent", "forwardx11", "permitlocalcommand", "requesttty", "controlmaster":
				if value != "no" {
					return "", fmt.Errorf("unsupported SSH option value")
				}
			case "remotecommand", "controlpath":
				if value != "none" {
					return "", fmt.Errorf("unsupported SSH option value")
				}
			case "stricthostkeychecking":
				if value != "yes" {
					return "", fmt.Errorf("host verification cannot be disabled")
				}
			case "compression":
				if value != "yes" && value != "no" {
					return "", fmt.Errorf("unsupported compression setting")
				}
			case "loglevel":
				if value != "quiet" && value != "error" && value != "fatal" {
					return "", fmt.Errorf("unsupported SSH logging setting")
				}
			default:
				return "", fmt.Errorf("unsupported managed SSH configuration")
			}
		}
	}
	if index >= len(args) {
		return "", fmt.Errorf("managed SSH host required")
	}
	host := args[index]
	index++
	if host != "fennec-cloud" && host != runtimeID+"@fennec-cloud" {
		return "", fmt.Errorf("managed SSH host cannot change")
	}
	if index >= len(args) {
		return "", fmt.Errorf("managed SSH command required")
	}
	if subsystem {
		if len(args)-index != 1 || args[index] != "sftp" {
			return "", fmt.Errorf("unsupported managed SSH subsystem")
		}
		return `"$HOME/.local/fennec/bin/fennec-terminal" sftp-stdio`, nil
	}
	return strings.Join(args[index:], " "), nil
}
