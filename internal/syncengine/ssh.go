package syncengine

import (
	"fennec/internal/cloudtarget"
	"fennec/internal/runta"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func quoteShell(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func quoteConfig(value string) (string, error) {
	if !filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n%") {
		return "", fmt.Errorf("invalid absolute SSH file path")
	}
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`, nil
}

// ConfigureSSH writes a private ssh/scp wrapper directory used by Mutagen's
// documented MUTAGEN_SSH_PATH. No ~/.ssh/config or system SSH settings change.
// Give each user/runtime a separate Manager/DataDirectory, since the Mutagen
// daemon inherits this environment at startup.
func ConfigureSSH(directory, runtimeID, identity, knownHosts string) (string, error) {
	return configureSSH(directory, runtimeID, identity, knownHosts, runta.ActiveSSHHost(), 22, runtimeID)
}

// ConfigureTargetSSH is ConfigureSSH for an explicit cloud target, such as a
// paired self-hosted computer with its own host, port and login.
func ConfigureTargetSSH(directory, runtimeID string, target cloudtarget.Target) (string, error) {
	return configureSSH(directory, runtimeID, target.Key(), target.KnownHosts(), target.Host, target.SSHPort(), target.Login(runtimeID))
}

func configureSSH(directory, runtimeID, identity, knownHosts, host string, port int, login string) (string, error) {
	if host == "" || strings.ContainsAny(host, " \t\r\n\"%\\") || port < 1 || port > 65535 || !regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,63}$`).MatchString(login) {
		return "", fmt.Errorf("invalid SSH target")
	}
	if !filepath.IsAbs(directory) || !regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`).MatchString(runtimeID) {
		return "", fmt.Errorf("invalid SSH scope")
	}
	key, err := quoteConfig(identity)
	if err != nil {
		return "", err
	}
	hosts, err := quoteConfig(knownHosts)
	if err != nil {
		return "", err
	}
	for _, path := range []string{identity, knownHosts} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("verified SSH material required")
		}
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	config := filepath.Join(directory, "config")
	text := "Host fennec-cloud\n  HostName " + host + "\n  Port " + strconv.Itoa(port) + "\n  IPQoS ef\n  User " + login + "\n  IdentityFile " + key + "\n  UserKnownHostsFile " + hosts + "\n  IdentitiesOnly yes\n  StrictHostKeyChecking yes\n  BatchMode yes\n  ForwardAgent no\n  ControlMaster no\n  ControlPath none\n  ServerAliveInterval 15\n  ServerAliveCountMax 3\n"
	if err = os.WriteFile(config, []byte(text), 0600); err != nil {
		return "", err
	}
	for _, tool := range []string{"ssh", "scp"} {
		script := "#!/bin/sh\nexec /usr/bin/" + tool + " -F " + quoteShell(config) + " \"$@\"\n"
		if err = os.WriteFile(filepath.Join(directory, tool), []byte(script), 0700); err != nil {
			return "", err
		}
	}
	return "fennec-cloud", nil
}
