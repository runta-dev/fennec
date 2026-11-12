package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// writeConfiguredMagpieModel updates an already-wired agent's Magpie model
// directly in Go (~2ms, avoiding cold-booting Magpie's Bun plugin host ~460ms)
// under magpie-config.lock after verifying the model against /v1/models.
// Returns handled=false when the agent is not yet wired to Magpie or the local
// gateway HTTP endpoint is unreachable, allowing callers to fall back cleanly
// to the `magpie` CLI.
func writeConfiguredMagpieModel(ctx context.Context, home, agentID, model string) (bool, error) {
	if !filepath.IsAbs(home) || !map[string]bool{"codex": true, "claude": true, "pi": true}[agentID] {
		return false, nil
	}
	if _, ok := readConfiguredMagpieModel(home, agentID); !ok {
		return false, nil
	}
	if _, err := ParseAgentModel("Agent model " + model); err != nil {
		return true, err
	}

	checkCtx, cancelCheck := context.WithTimeout(ctx, 2*time.Second)
	available, catErr := LocalGatewayModels(checkCtx)
	cancelCheck()
	if catErr != nil {
		return false, nil
	}
	exposed := false
	for _, id := range available {
		if id == model {
			exposed = true
			break
		}
	}
	if !exposed {
		return true, fmt.Errorf("selected model is not exposed by the gateway")
	}

	lockCtx, cancelLock := context.WithTimeout(ctx, 30*time.Second)
	defer cancelLock()
	lockDir := filepath.Join(home, ".local/fennec/install-locks")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return false, nil
	}
	lockFile, err := os.OpenFile(filepath.Join(lockDir, "magpie-config.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return false, nil
	}
	defer lockFile.Close()
	for {
		if err := lockCtx.Err(); err != nil {
			return true, err
		}
		err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return false, nil
		}
		select {
		case <-lockCtx.Done():
			return true, lockCtx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	switch agentID {
	case "codex":
		cfgPath := filepath.Join(resolveAgentConfigDir(home, "CODEX_HOME", ".codex"), "config.toml")
		previous, err := readNativeFile(cfgPath)
		if err != nil {
			return false, nil
		}
		var cfg map[string]any
		if toml.Unmarshal(previous, &cfg) != nil || cfg == nil {
			return false, nil
		}
		provider, _ := cfg["model_provider"].(string)
		baseURL, _ := cfg["openai_base_url"].(string)
		if provider != "magpie" && !isLocalMagpieGatewayURL(baseURL) {
			return false, nil
		}
		cfg["model"] = model
		updated, err := toml.Marshal(cfg)
		if err != nil {
			return false, nil
		}
		if err := writeAtomicAgentFile(cfgPath, updated); err != nil {
			return true, err
		}
		updateMagpieAppliedModel(home, "codex", model)
		return true, nil

	case "claude":
		cfgPath := filepath.Join(resolveAgentConfigDir(home, "CLAUDE_CONFIG_DIR", ".claude"), "settings.json")
		previous, err := readNativeFile(cfgPath)
		if err != nil {
			return false, nil
		}
		var cfg map[string]any
		if json.Unmarshal(previous, &cfg) != nil || cfg == nil {
			return false, nil
		}
		env, _ := cfg["env"].(map[string]any)
		if env == nil {
			return false, nil
		}
		baseURL, _ := env["ANTHROPIC_BASE_URL"].(string)
		if !isLocalMagpieGatewayURL(baseURL) {
			return false, nil
		}
		cfg["model"] = model
		for _, k := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"} {
			if _, exists := env[k]; exists {
				env[k] = model
			}
		}
		updated, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return false, nil
		}
		updated = append(updated, '\n')
		if err := writeAtomicAgentFile(cfgPath, updated); err != nil {
			return true, err
		}
		updateMagpieAppliedModel(home, "claude", model)
		return true, nil

	case "pi":
		cfgPath := filepath.Join(resolveAgentConfigDir(home, "PI_CODING_AGENT_DIR", ".pi/agent"), "settings.json")
		previous, err := readNativeFile(cfgPath)
		if err != nil {
			return false, nil
		}
		var cfg map[string]any
		if json.Unmarshal(previous, &cfg) != nil || cfg == nil {
			return false, nil
		}
		defProv, _ := cfg["defaultProvider"].(string)
		defModel, _ := cfg["defaultModel"].(string)
		if defProv != "magpie" && !strings.HasPrefix(defModel, "magpie/") {
			return false, nil
		}
		if strings.HasPrefix(defModel, "magpie/") {
			cfg["defaultModel"] = "magpie/" + model
		} else {
			cfg["defaultModel"] = model
		}
		updated, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return false, nil
		}
		updated = append(updated, '\n')
		if err := writeAtomicAgentFile(cfgPath, updated); err != nil {
			return true, err
		}
		updateMagpieAppliedModel(home, "pi", "magpie/"+model)
		return true, nil
	}
	return false, nil
}

func writeAtomicAgentFile(path string, data []byte) error {
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fennec-model-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func updateMagpieAppliedModel(home, agentID, appliedModel string) {
	appliedPath := filepath.Join(home, ".config", "magpie", "applied.json")
	raw, err := os.ReadFile(appliedPath)
	var doc map[string]any
	if err == nil && len(raw) <= 1<<20 {
		_ = json.Unmarshal(raw, &doc)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	entry, _ := doc[agentID].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	fields, _ := entry["fields"].(map[string]any)
	if fields == nil {
		fields = map[string]any{}
	}
	fields["model"] = appliedModel
	entry["fields"] = fields
	entry["at"] = time.Now().UTC().Format(time.RFC3339Nano)
	doc[agentID] = entry
	if encoded, err := json.MarshalIndent(doc, "", "  "); err == nil {
		_ = os.MkdirAll(filepath.Dir(appliedPath), 0700)
		_ = writeAtomicAgentFile(appliedPath, append(encoded, '\n'))
	}
}
