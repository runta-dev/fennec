package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"
)

// Use the same file precedence as Magpie, including OpenCode's JSONC files.
func openCodeMagpieConfig(home string) (string, hujson.Value, map[string]any, bool) {
	dir := filepath.Join(resolveAgentConfigDir(home, "XDG_CONFIG_HOME", ".config"), "opencode")
	if override := os.Getenv("OPENCODE_CONFIG_DIR"); filepath.IsAbs(override) {
		dir = override
	}
	for _, name := range []string{"opencode.jsonc", "opencode.json", "config.json"} {
		path := filepath.Join(dir, name)
		data, err := readNativeFile(path)
		if os.IsNotExist(err) {
			continue
		}
		doc, parseErr := hujson.Parse(data)
		if err != nil || parseErr != nil {
			return "", hujson.Value{}, nil, false
		}
		plain := doc.Clone()
		plain.Standardize()
		var cfg map[string]any
		if json.Unmarshal(plain.Pack(), &cfg) != nil {
			return "", hujson.Value{}, nil, false
		}
		return path, doc, cfg, true
	}
	return "", hujson.Value{}, nil, false
}

func openCodeMagpieProvider(cfg map[string]any) map[string]any {
	providers, _ := cfg["provider"].(map[string]any)
	provider, _ := providers["magpie"].(map[string]any)
	options, _ := provider["options"].(map[string]any)
	// OpenCode 2's provider replaces the legacy entry as a whole.
	if providers, ok := cfg["providers"].(map[string]any); ok {
		if entry, exists := providers["magpie"]; exists {
			provider, _ = entry.(map[string]any)
			options, _ = provider["settings"].(map[string]any)
		}
	}
	if !isLocalMagpieGatewayURL(stringValue(options["baseURL"])) || stringValue(options["apiKey"]) == "" {
		return nil
	}
	return provider
}

func readConfiguredOpenCodeModel(home string) (string, bool) {
	_, _, cfg, ok := openCodeMagpieConfig(home)
	if !ok || openCodeMagpieProvider(cfg) == nil || !strings.HasPrefix(stringValue(cfg["model"]), "magpie/") {
		return "", false
	}
	model, err := ParseAgentModel("OpenCode model " + stringValue(cfg["model"]))
	return model, err == nil
}

// The caller holds the shared configuration lock and verified /v1/models.
// Patch only the selected model, preserving comments, model order and options.
func writeConfiguredOpenCodeModel(home, model string) (bool, error) {
	path, doc, cfg, ok := openCodeMagpieConfig(home)
	if !ok || !strings.HasPrefix(stringValue(cfg["model"]), "magpie/") {
		return false, nil
	}
	provider := openCodeMagpieProvider(cfg)
	models, _ := provider["models"].(map[string]any)
	if _, exists := models[model]; !exists || provider == nil {
		return false, nil
	}
	patch, _ := json.Marshal([]map[string]any{{"op": "replace", "path": "/model", "value": "magpie/" + model}})
	if err := doc.Patch(patch); err != nil {
		return true, err
	}
	if err := writeAtomicAgentFile(path, doc.Pack()); err != nil {
		return true, err
	}
	updateMagpieAppliedModel(home, "opencode", "magpie/"+model)
	return true, nil
}
