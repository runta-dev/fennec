package setup

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

func isLocalMagpieGatewayURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return (host == "127.0.0.1" || host == "localhost") && parsed.Port() == "3425"
}

func resolveAgentConfigDir(home, envKey, relFallback string) string {
	if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
		if strings.HasPrefix(value, "~/") && filepath.IsAbs(home) {
			return filepath.Join(home, value[2:])
		}
		if filepath.IsAbs(value) {
			return filepath.Clean(value)
		}
	}
	return filepath.Join(home, relFallback)
}

// readConfiguredMagpieModel inspects the agent's own configuration file when it
// is explicitly routed to the local Magpie gateway (:3425 / provider "magpie").
// Callers fall back to invoking the magpie CLI when this returns false.
func readConfiguredMagpieModel(home, agentID string) (string, bool) {
	if !filepath.IsAbs(home) {
		return "", false
	}
	switch agentID {
	case "grok":
		path := filepath.Join(resolveAgentConfigDir(home, "GROK_HOME", ".grok"), "config.toml")
		data, err := readNativeFile(path)
		var cfg struct {
			Models struct {
				Default string `toml:"default"`
			} `toml:"models"`
			Model map[string]struct {
				Model   string `toml:"model"`
				BaseURL string `toml:"base_url"`
				APIKey  string `toml:"api_key"`
			} `toml:"model"`
		}
		if err != nil || toml.Unmarshal(data, &cfg) != nil || !strings.HasPrefix(cfg.Models.Default, "magpie/") {
			return "", false
		}
		selected, ok := cfg.Model[cfg.Models.Default]
		if !ok || !isLocalMagpieGatewayURL(selected.BaseURL) || selected.APIKey == "" {
			return "", false
		}
		model, err := ParseAgentModel("Grok Build model " + cfg.Models.Default)
		return model, err == nil && selected.Model == model
	case "codex":
		data, err := readNativeFile(filepath.Join(resolveAgentConfigDir(home, "CODEX_HOME", ".codex"), "config.toml"))
		if err != nil {
			return "", false
		}
		var cfg map[string]any
		if toml.Unmarshal(data, &cfg) != nil {
			return "", false
		}
		provider, _ := cfg["model_provider"].(string)
		baseURL, _ := cfg["openai_base_url"].(string)
		if provider != "magpie" && !isLocalMagpieGatewayURL(baseURL) {
			return "", false
		}
		rawModel, _ := cfg["model"].(string)
		model, err := ParseAgentModel("Codex model " + rawModel)
		if err != nil {
			return "", false
		}
		return model, true
	case "claude":
		data, err := readNativeFile(filepath.Join(resolveAgentConfigDir(home, "CLAUDE_CONFIG_DIR", ".claude"), "settings.json"))
		if err != nil {
			return "", false
		}
		var cfg struct {
			Model string            `json:"model"`
			Env   map[string]string `json:"env"`
		}
		if json.Unmarshal(data, &cfg) != nil || !isLocalMagpieGatewayURL(cfg.Env["ANTHROPIC_BASE_URL"]) {
			return "", false
		}
		rawModel := cfg.Model
		if rawModel == "" {
			rawModel = cfg.Env["ANTHROPIC_MODEL"]
		}
		model, err := ParseAgentModel("Claude Code model " + rawModel)
		if err != nil {
			return "", false
		}
		return model, true
	case "pi":
		data, err := readNativeFile(filepath.Join(resolveAgentConfigDir(home, "PI_CODING_AGENT_DIR", ".pi/agent"), "settings.json"))
		if err != nil {
			return "", false
		}
		var cfg struct {
			DefaultProvider string `json:"defaultProvider"`
			DefaultModel    string `json:"defaultModel"`
		}
		if json.Unmarshal(data, &cfg) != nil {
			return "", false
		}
		if cfg.DefaultProvider != "magpie" && !strings.HasPrefix(cfg.DefaultModel, "magpie/") {
			return "", false
		}
		model, err := ParseAgentModel("Pi model " + cfg.DefaultModel)
		if err != nil {
			return "", false
		}
		return model, true
	default:
		return "", false
	}
}

func readConfiguredMagpieModelOutput(home, agentID string) ([]byte, bool) {
	model, ok := readConfiguredMagpieModel(home, agentID)
	if !ok {
		return nil, false
	}
	label := map[string]string{"claude": "Claude Code", "codex": "Codex", "pi": "Pi", "kimi": "Kimi Code"}[agentID]
	if label == "" {
		label = agentID
	}
	return []byte(label + " model " + model + "\n"), true
}
