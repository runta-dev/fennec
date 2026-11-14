package setup

import (
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// The caller holds magpie-config.lock and has verified the gateway catalog.
// Magpie owns the model tables, including API choice, keys and context limits.
// A missing table needs its configuration writer; an existing one switches in Go.
func writeConfiguredGrokModel(home, model string) (bool, error) {
	path := filepath.Join(resolveAgentConfigDir(home, "GROK_HOME", ".grok"), "config.toml")
	data, err := readNativeFile(path)
	var cfg map[string]any
	if err != nil || toml.Unmarshal(data, &cfg) != nil {
		return false, nil
	}
	tables, _ := cfg["model"].(map[string]any)
	selected, _ := tables["magpie/"+model].(map[string]any)
	if stringValue(selected["model"]) != model || !isLocalMagpieGatewayURL(stringValue(selected["base_url"])) || stringValue(selected["api_key"]) == "" {
		return false, nil
	}
	defaults, _ := cfg["models"].(map[string]any)
	if defaults == nil {
		defaults = map[string]any{}
		cfg["models"] = defaults
	}
	defaults["default"] = "magpie/" + model
	if effort := stringValue(defaults["default_reasoning_effort"]); effort != "" {
		valid := false
		if efforts, ok := selected["reasoning_efforts"].([]any); ok {
			for _, candidate := range efforts {
				valid = valid || candidate == effort
			}
		}
		if !valid {
			delete(defaults, "default_reasoning_effort")
		}
	}
	features, _ := cfg["features"].(map[string]any)
	if features == nil {
		features = map[string]any{}
		cfg["features"] = features
	}
	// Grok's remote campaigns otherwise override the explicitly selected model.
	features["campaigns"] = false
	updated, err := toml.Marshal(cfg)
	if err != nil {
		return true, err
	}
	if err := writeAtomicAgentFile(path, updated); err != nil {
		return true, err
	}
	updateMagpieAppliedModel(home, "grok", "magpie/"+model)
	return true, nil
}
