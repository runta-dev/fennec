package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	nativeSourceEnvMu          sync.Mutex
	nativeSourceEnvFingerprint string
	nativeSourceEnvExpires     time.Time
	nativeSourceEnvValues      map[string]string
)

func shellStartupFingerprint(keys []string, shell string) string {
	var b strings.Builder
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	zdot := os.Getenv("ZDOTDIR")
	b.WriteString(home)
	b.WriteByte(0)
	b.WriteString(shell)
	b.WriteByte(0)
	for _, key := range keys {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(os.Getenv(key))
		b.WriteByte(0)
	}
	paths := []string{shell}
	for _, dir := range []string{home, zdot} {
		if dir == "" {
			continue
		}
		for _, name := range []string{".zshenv", ".zprofile", ".zshrc", ".zlogin", ".bashrc", ".bash_profile", ".bash_login", ".profile"} {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	for _, p := range paths {
		b.WriteString(p)
		if info, err := os.Stat(p); err == nil {
			b.WriteByte(':')
			b.WriteString(strconv.FormatInt(info.Size(), 10))
			b.WriteByte(':')
			b.WriteString(strconv.FormatInt(info.ModTime().UnixNano(), 10))
		} else {
			b.WriteString(":missing")
		}
		b.WriteByte(0)
	}
	return b.String()
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// A LaunchAgent does not inherit interactive shell API keys or custom config
// roots. Read only this fixed allowlist; never print it or execute credential
// helper commands referenced inside agent configuration.
func nativeSourceEnvironment(ctx context.Context) map[string]string {
	keys := append(append([]string{}, nativeEnvKeys...), "CODEX_HOME", "CLAUDE_CONFIG_DIR", "PI_CODING_AGENT_DIR", "KIMI_CODE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "ZDOTDIR", "USER")
	result := map[string]string{}
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			result[key] = value
		}
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	if !filepath.IsAbs(shell) {
		return result
	}
	fingerprint := shellStartupFingerprint(keys, shell)
	now := time.Now()
	nativeSourceEnvMu.Lock()
	if fingerprint == nativeSourceEnvFingerprint && now.Before(nativeSourceEnvExpires) && nativeSourceEnvValues != nil {
		cached := copyStringMap(nativeSourceEnvValues)
		nativeSourceEnvMu.Unlock()
		return cached
	}
	nativeSourceEnvMu.Unlock()
	var script strings.Builder
	script.WriteString("printf '\\0'; printf '%s\\0'")
	for _, key := range keys {
		script.WriteString(" \"")
		script.WriteString(key)
		script.WriteString("=$")
		script.WriteString(key)
		script.WriteString("\"")
	}
	step, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(step, shell, "-ilc", script.String())
	command.WaitDelay = time.Second
	output := &nativeEnvironmentOutput{}
	command.Stdout = output
	if command.Run() != nil {
		return result
	}
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	for _, entry := range strings.Split(output.String(), "\x00") {
		key, value, ok := strings.Cut(entry, "=")
		if ok && allowed[key] && value != "" {
			result[key] = value
		}
	}
	nativeSourceEnvMu.Lock()
	nativeSourceEnvFingerprint = fingerprint
	nativeSourceEnvExpires = time.Now().Add(5 * time.Minute)
	nativeSourceEnvValues = copyStringMap(result)
	nativeSourceEnvMu.Unlock()
	return result
}

// Fail closed rather than silently truncating a long API key or session token.
type nativeEnvironmentOutput struct{ strings.Builder }

func (b *nativeEnvironmentOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 256<<10 {
		return 0, fmt.Errorf("native environment exceeds size limit")
	}
	return b.Builder.Write(p)
}

// LocalShellDirectories returns only shell configuration locations, never keys.
func LocalShellDirectories(ctx context.Context) (string, string) {
	env := nativeSourceEnvironment(ctx)
	return env["ZDOTDIR"], env["XDG_CONFIG_HOME"]
}
