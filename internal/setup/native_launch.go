package setup

import (
	"encoding/json"
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
	if !map[string]bool{"codex": true, "claude": true, "pi": true, "opencode": true}[agent] {
		return fmt.Errorf("unsupported native agent")
	}
	binary, err := exec.LookPath(agent)
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
		if !allowed[key] && !nativeInheritedModelOverride(key) && key != "CODEX_HOME" && key != "CLAUDE_CONFIG_DIR" && key != "PI_CODING_AGENT_DIR" && key != "XDG_CONFIG_HOME" && key != "XDG_DATA_HOME" {
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
	case "codex":
		env = append(env, "CODEX_HOME="+filepath.Join(root, "codex"))
	case "claude":
		env = append(env, "CLAUDE_CONFIG_DIR="+filepath.Join(root, "claude"))
	case "pi":
		env = append(env, "PI_CODING_AGENT_DIR="+filepath.Join(root, "pi"))
	case "opencode":
		env = append(env, "XDG_CONFIG_HOME="+filepath.Join(root, "opencode/config"), "XDG_DATA_HOME="+filepath.Join(root, "opencode/data"))
	}
	return env, nil
}
