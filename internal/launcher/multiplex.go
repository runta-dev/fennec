package launcher

import (
	"crypto/sha256"
	"fennec/internal/cloudtarget"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

func SessionControlPath(directory, runtimeID, sessionID string) (string, error) {
	if !filepath.IsAbs(directory) || !regexp.MustCompile(`^[a-f0-9-]{36}$`).MatchString(runtimeID) || !regexp.MustCompile(`^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`).MatchString(sessionID) {
		return "", fmt.Errorf("invalid SSH session identity")
	}
	digest := sha256.Sum256([]byte(runtimeID + ":" + sessionID))
	return filepath.Join(directory, "mux", fmt.Sprintf("%x", digest[:12])), nil
}

func LaunchSSHArgs(directory, runtimeID, sessionID string) ([]string, error) {
	control, err := SessionControlPath(directory, runtimeID, sessionID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(control), 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Dir(control))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("SSH control directory must be private")
	}
	args := []string{"-T", "-S", control, "-o", "ControlMaster=auto", "-o", "ControlPersist=60", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "ConnectTimeout=20", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=8"}
	target, err := cloudtarget.ForDir(directory)
	if err != nil {
		return nil, err
	}
	args = append(args, SSHTransportOptions(target.Home())...)
	args = append(args, target.OpenSSHArgs(runtimeID)...)
	args = append(args, "sh -s")
	return args, nil
}
