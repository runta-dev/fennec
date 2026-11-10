// Package syncengine delegates project synchronization to Mutagen.
package syncengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Endpoint struct {
	Protocol           string            `json:"protocol"`
	Path               string            `json:"path"`
	Host               string            `json:"host"`
	User               string            `json:"user"`
	Connected          bool              `json:"connected"`
	Scanned            bool              `json:"scanned"`
	ScanProblems       []json.RawMessage `json:"scanProblems"`
	TransitionProblems []json.RawMessage `json:"transitionProblems"`
}
type IgnorePolicy struct {
	Paths []string `json:"paths"`
	VCS   bool     `json:"vcs"`
}
type SymlinkPolicy struct {
	Mode string `json:"mode"`
}

var defaultIgnores = []string{"node_modules", ".venv", "__pycache__", ".DS_Store", ".env", ".env.*", ".ssh", ".aws", ".gnupg", ".npmrc", ".netrc", "*.pem", "*.key"}

type Session struct {
	Ignore           IgnorePolicy      `json:"ignore"`
	Symlink          SymlinkPolicy     `json:"symlink"`
	Identifier       string            `json:"identifier"`
	Name             string            `json:"name"`
	Mode             string            `json:"mode"`
	Status           string            `json:"status"`
	Paused           bool              `json:"paused"`
	SuccessfulCycles uint64            `json:"successfulCycles"`
	Conflicts        []json.RawMessage `json:"conflicts"`
	LastError        string            `json:"lastError"`
	Alpha            Endpoint          `json:"alpha"`
	Beta             Endpoint          `json:"beta"`
}

// Ready is file synchronization evidence, not full offline-task readiness.
func (s Session) Ready() bool {
	return s.Mode == "two-way-safe" && !s.Paused && s.Status == "watching" && s.SuccessfulCycles > 0 && s.Alpha.Connected && s.Beta.Connected && s.Alpha.Scanned && s.Beta.Scanned && s.LastError == "" && len(s.Conflicts) == 0 && len(s.Alpha.ScanProblems)+len(s.Beta.ScanProblems)+len(s.Alpha.TransitionProblems)+len(s.Beta.TransitionProblems) == 0
}

type Manager struct{ Binary, DataDirectory, SSHDirectory string }

func (m Manager) run(ctx context.Context, args ...string) ([]byte, error) {
	if !filepath.IsAbs(m.DataDirectory) {
		return nil, fmt.Errorf("absolute Fennec Mutagen data directory required")
	}
	if runtime.GOOS == "darwin" && len(filepath.Join(m.DataDirectory, "daemon", "daemon.sock")) >= 104 {
		return nil, fmt.Errorf("Mutagen data directory exceeds macOS socket path limit; use a shorter private directory")
	}
	if err := os.MkdirAll(m.DataDirectory, 0700); err != nil {
		return nil, err
	}
	binary := m.Binary
	if binary == "" {
		binary = "mutagen"
	}
	command := exec.CommandContext(ctx, binary, args...)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "MUTAGEN_DATA_DIRECTORY=") && !strings.HasPrefix(v, "MUTAGEN_SSH_PATH=") {
			command.Env = append(command.Env, v)
		}
	}
	command.Env = append(command.Env, "MUTAGEN_DATA_DIRECTORY="+m.DataDirectory)
	if m.SSHDirectory != "" {
		if !filepath.IsAbs(m.SSHDirectory) {
			return nil, fmt.Errorf("absolute SSH wrapper directory required")
		}
		command.Env = append(command.Env, "MUTAGEN_SSH_PATH="+m.SSHDirectory)
	}
	data, err := command.Output()
	if err != nil {
		var failure *exec.ExitError
		if errors.As(err, &failure) {
			detail := string(failure.Stderr)
			if len(detail) > 2048 {
				detail = detail[len(detail)-2048:]
			}
			return nil, fmt.Errorf("Mutagen %s failed: %w: %s", args[0], err, detail)
		}
		return nil, fmt.Errorf("Mutagen %s failed: %w", args[0], err)
	}
	return data, nil
}
func SessionName(user, root, beta string) (string, error) {
	if user == "" || !filepath.IsAbs(root) || beta == "" || strings.ContainsAny(beta, "\x00\r\n") || strings.HasPrefix(beta, "-") {
		return "", fmt.Errorf("user, absolute project root and valid endpoint required")
	}
	digest := sha256.Sum256([]byte(user + "\x00" + filepath.Clean(root) + "\x00" + beta))
	return "fennec-" + hex.EncodeToString(digest[:16]), nil
}
func (m Manager) Inspect(ctx context.Context, name string) ([]Session, error) {
	if !strings.HasPrefix(name, "fennec-") || strings.ContainsAny(name, "/\x00\r\n ") {
		return nil, fmt.Errorf("invalid Fennec sync name")
	}
	data, err := m.run(ctx, "sync", "list", name, "--template", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var sessions []Session
	if err = json.Unmarshal(data, &sessions); err != nil {
		return nil, fmt.Errorf("invalid Mutagen state: %w", err)
	}
	return sessions, nil
}

// Ensure never rewrites an existing session's endpoints or conflict policy.
// beta is a preconfigured SSH endpoint or an absolute path for isolated tests.
func (m Manager) Ensure(ctx context.Context, user, root, beta string) (Session, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Session{}, err
	}
	ignores, err := ProjectIgnores(real)
	if err != nil {
		return Session{}, err
	}
	name, err := SessionName(user, real, beta)
	if err != nil {
		return Session{}, err
	}
	if _, err = m.run(ctx, "daemon", "start"); err != nil {
		return Session{}, err
	}
	// List all in our private daemon: a missing named selection is an error in
	// Mutagen, whereas an empty daemon is a valid initial condition.
	data, err := m.run(ctx, "sync", "list", "--template", "{{json .}}")
	if err != nil {
		return Session{}, err
	}
	var sessions []Session
	if err = json.Unmarshal(data, &sessions); err != nil {
		return Session{}, err
	}
	for _, s := range sessions {
		if s.Name == name {
			if !matchesConfiguration(s, real, beta, ignores...) {
				return Session{}, fmt.Errorf("existing sync configuration differs")
			}
			return s, nil
		}
	}
	args := []string{"sync", "create", "--name", name, "--no-global-configuration", "--mode", "two-way-safe", "--hash", "sha256", "--ignore-vcs", "--symlink-mode", "ignore", "--max-entry-count", "20000", "--max-staging-file-size", "16MB"}
	// Apply project rules on both endpoints, including future files.
	for _, pattern := range ignores {
		args = append(args, "--ignore", pattern)
	}
	args = append(args, real, beta)
	if _, err = m.run(ctx, args...); err != nil {
		return Session{}, err
	}
	result, err := m.Inspect(ctx, name)
	if err != nil {
		return Session{}, err
	}
	if len(result) != 1 {
		return Session{}, fmt.Errorf("expected one synchronization session")
	}
	if !matchesConfiguration(result[0], real, beta, ignores...) {
		return Session{}, fmt.Errorf("created sync configuration differs")
	}
	return result[0], nil
}
func (m Manager) Flush(ctx context.Context, name string) (Session, error) {
	sessions, err := m.Inspect(ctx, name)
	if err != nil {
		return Session{}, err
	}
	if len(sessions) != 1 || sessions[0].Mode != "two-way-safe" {
		return Session{}, fmt.Errorf("invalid synchronization session")
	}
	if err := flushBlocker(sessions[0]); err != nil {
		return sessions[0], err
	}
	if _, err = m.run(ctx, "sync", "flush", sessions[0].Identifier); err != nil {
		return Session{}, err
	}
	sessions, err = m.Inspect(ctx, name)
	if err != nil {
		return Session{}, err
	}
	if len(sessions) != 1 {
		return Session{}, fmt.Errorf("sync session disappeared")
	}
	return sessions[0], nil
}

func matchesConfiguration(s Session, root, beta string, ignores ...string) bool {
	if len(ignores) == 0 {
		ignores = defaultIgnores
	}
	if s.Mode != "two-way-safe" || s.Alpha.Protocol != "local" || s.Alpha.Path != root || !s.Ignore.VCS || s.Symlink.Mode != "ignore" {
		return false
	}
	if len(s.Ignore.Paths) != len(ignores) {
		return false
	}
	for i, rule := range ignores {
		if s.Ignore.Paths[i] != rule {
			return false
		}
	}
	if filepath.IsAbs(beta) {
		return s.Beta.Protocol == "local" && s.Beta.Path == beta
	}
	host, path, ok := strings.Cut(beta, ":")
	return ok && host != "" && s.Beta.Protocol == "ssh" && s.Beta.Host == host && s.Beta.Path == path && s.Beta.User == ""
}

// SetPaused changes one explicitly selected Fennec session, never --all.
func (m Manager) SetPaused(ctx context.Context, name string, paused bool) (Session, error) {
	sessions, err := m.Inspect(ctx, name)
	if err != nil {
		return Session{}, err
	}
	if len(sessions) != 1 || sessions[0].Mode != "two-way-safe" {
		return Session{}, fmt.Errorf("one safe Fennec session required")
	}
	if sessions[0].Paused == paused {
		return sessions[0], nil
	}
	action := "resume"
	if paused {
		action = "pause"
	}
	if _, err = m.run(ctx, "sync", action, sessions[0].Identifier); err != nil {
		return Session{}, err
	}
	sessions, err = m.Inspect(ctx, name)
	if err != nil {
		return Session{}, err
	}
	if len(sessions) != 1 || sessions[0].Paused != paused {
		return Session{}, fmt.Errorf("pause state not confirmed")
	}
	return sessions[0], nil
}

// Remove stops only this session. Files on both endpoints are retained. A final
// safe cycle is required; use pause (not removal) while disconnected/conflicted.
func (m Manager) Remove(ctx context.Context, name string) error {
	current, err := m.Flush(ctx, name)
	if err != nil {
		return err
	}
	if !current.Ready() {
		return fmt.Errorf("sync has pending work or conflicts; removal refused, use pause to suspend")
	}
	if _, err = m.run(ctx, "sync", "terminate", current.Identifier); err != nil {
		return err
	}
	data, err := m.run(ctx, "sync", "list", "--template", "{{json .}}")
	if err != nil {
		return err
	}
	var sessions []Session
	if err = json.Unmarshal(data, &sessions); err != nil {
		return err
	}
	for _, session := range sessions {
		if session.Identifier == current.Identifier {
			return fmt.Errorf("sync removal not confirmed")
		}
	}
	return nil
}

// Known terminal scan failures and paused sessions cannot complete a flush.
func flushBlocker(s Session) error {
	if s.Paused {
		return fmt.Errorf("synchronization is paused; resume before flushing")
	}
	if s.LastError != "" {
		return fmt.Errorf("synchronization needs attention: %s", s.LastError)
	}
	if len(s.Alpha.ScanProblems)+len(s.Beta.ScanProblems) > 0 {
		return fmt.Errorf("synchronization scan failed; inspect excluded paths and file limits")
	}
	return nil
}
