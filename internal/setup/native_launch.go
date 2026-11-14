package setup

import (
	"encoding/json"
	agents "fennec/internal/agentcmd"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// ConfigureAgentMode is account-scoped by the verified sandbox owner. Existing
// agent processes retain their own environment when the integration is toggled.
func ConfigureAgentMode(home string, native bool) error {
	root := filepath.Join(home, ".local/fennec")
	if err := ensureNativeDirectory(home, ".local/fennec"); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]bool{"native": native})
	return atomicCredential(filepath.Join(root, "agent-mode.json"), b)
}
func NativeAgentMode(home string) bool {
	var mode struct {
		Native bool `json:"native"`
	}
	b, e := os.ReadFile(filepath.Join(home, ".local/fennec/agent-mode.json"))
	// No migration file means native mode, not implicit gateway activation.
	return e != nil || json.Unmarshal(b, &mode) != nil || mode.Native
}
func ExecNativeAgent(home, agent string, args []string) error {
	if !map[string]bool{"codex": true, "claude": true, "pi": true, "opencode": true, "kimi": true, "cursor": true, "grok": true}[agent] {
		return fmt.Errorf("unsupported native agent")
	}
	ensureCloudSearchTools(home)
	binary, err := exec.LookPath(agents.BinaryName(agent))
	if err != nil {
		return fmt.Errorf("native agent binary missing")
	}
	env, err := nativeAgentEnvironment(home, agent)
	if err != nil {
		return err
	}
	return syscall.Exec(binary, append([]string{binary}, args...), env)
}

func nativeAgentEnvironment(home, agent string) ([]string, error) {
	root, _, err := NativeAccountRoot(home, agent)
	if err != nil {
		return nil, err
	}
	var values map[string]string
	data, err := readNativeFile(filepath.Join(root, "env.json"))
	if err != nil {
		return nil, fmt.Errorf("native environment is unavailable")
	}
	if json.Unmarshal(data, &values) != nil {
		return nil, fmt.Errorf("invalid native environment")
	}
	allowed := map[string]bool{}
	for _, key := range nativeEnvKeys {
		allowed[key] = true
	}
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !allowed[key] && !nativeInheritedModelOverride(key) && key != "CODEX_HOME" && key != "CLAUDE_CONFIG_DIR" && key != "PI_CODING_AGENT_DIR" && key != kimiHomeEnv && key != "AGENT_CLI_CREDENTIAL_STORE" && !strings.HasPrefix(key, "CURSOR_") && !strings.HasPrefix(key, "GROK_") && !strings.HasPrefix(key, "OPENCODE_CONFIG") && key != "XDG_CONFIG_HOME" && key != "XDG_DATA_HOME" {
			env = append(env, entry)
		}
	}
	oauth := nativeOAuthIdentity(agent, func(name string) []byte { data, _ := readNativeFile(filepath.Join(root, name)); return data })
	for key, value := range values {
		if !allowed[key] || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("invalid native environment key")
		}
		if !nativeEnvForAgent(agent, key) || (oauth && nativeOAuthOverride(agent, key)) {
			continue
		}
		env = append(env, key+"="+value)
	}
	switch agent {
	case "cursor":
		env = append(env, "CURSOR_CONFIG_DIR="+filepath.Join(root, "cursor"), "XDG_CONFIG_HOME="+root, "AGENT_CLI_CREDENTIAL_STORE=file", "CURSOR_DATA_DIR="+filepath.Join(root, "cursor"))
	case "grok":
		env = append(env, "GROK_HOME="+filepath.Join(root, "grok"))
	case "codex":
		env = append(env, "CODEX_HOME="+filepath.Join(root, "codex"))
	case "claude":
		env = append(env, "CLAUDE_CONFIG_DIR="+filepath.Join(root, "claude"))
	case "pi":
		env = append(env, "PI_CODING_AGENT_DIR="+filepath.Join(root, "pi"))
	case "kimi":
		env = append(env, kimiHomeEnv+"="+filepath.Join(root, "kimi"))
	case "opencode":
		env = append(env, "XDG_CONFIG_HOME="+filepath.Join(root, "opencode/config"), "XDG_DATA_HOME="+filepath.Join(root, "opencode/data"), "OPENCODE_CONFIG_DIR="+filepath.Join(root, "opencode/config/opencode"))
	}
	env = ensureDefaultSystemCerts(env)
	return env, nil
}

func ensureDefaultSystemCerts(env []string) []string {
	has := map[string]bool{}
	for _, entry := range env {
		key, val, ok := strings.Cut(entry, "=")
		if ok && val != "" {
			has[key] = true
		}
	}
	if !has["NODE_EXTRA_CA_CERTS"] {
		if info, err := os.Stat("/usr/local/share/ca-certificates/runta-egress.crt"); err == nil && info.Mode().IsRegular() {
			env = append(env, "NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/runta-egress.crt")
		}
	}
	if !has["SSL_CERT_FILE"] {
		if info, err := os.Stat("/etc/ssl/certs/ca-certificates.crt"); err == nil && info.Mode().IsRegular() {
			env = append(env, "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt")
		}
	}
	return env
}

// ensureCloudSearchTools exposes an existing system or Pi-bundled fd binary in
// ~/.local/bin/fd so isolated agent homes/PI_CODING_AGENT_DIR find fd on PATH.
func ensureCloudSearchTools(home string) {
	if !filepath.IsAbs(home) {
		return
	}
	localBin := filepath.Join(home, ".local/bin")
	target := filepath.Join(localBin, "fd")
	if info, err := os.Stat(target); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
		return
	}
	if path, err := exec.LookPath("fd"); err == nil && path != "" {
		return
	}
	candidates := []string{}
	if fdfind, err := exec.LookPath("fdfind"); err == nil && fdfind != "" {
		candidates = append(candidates, fdfind)
	}
	candidates = append(candidates, filepath.Join(home, ".pi/agent/bin/fd"))
	for _, candidate := range candidates {
		info, err := os.Lstat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			continue
		}
		if os.MkdirAll(localBin, 0755) != nil {
			return
		}
		tmp := fmt.Sprintf("%s.tmp.%d", target, os.Getpid())
		_ = os.Remove(tmp)
		if os.Symlink(candidate, tmp) == nil {
			if os.Rename(tmp, target) != nil {
				_ = os.Remove(tmp)
			}
		}
		return
	}
}

// NativeAgentEnvironment is the environment ExecNativeAgent would use; the
// cloud codex app-server reuses it so desktop and terminal sessions match.
func NativeAgentEnvironment(home, agent string) ([]string, error) {
	return nativeAgentEnvironment(home, agent)
}
