package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// chatgptRoutingKeys are owned by the Magpie gateway configuration. ChatGPT's
// own config.toml takes them from the gateway on every connection and keeps
// everything else ChatGPT wrote there.
var chatgptRoutingKeys = []string{"model", "model_provider", "model_providers", "openai_base_url", "model_reasoning_effort", "model_reasoning_summary", "model_verbosity", "model_catalog_json", "model_context_window"}

// desktopOnlyCommand reports MCP servers ChatGPT.app registers with Mac
// executables, which cannot start on the cloud computer.
func desktopOnlyCommand(command string) bool {
	return strings.HasPrefix(command, "/Applications/ChatGPT.app/") || strings.HasPrefix(command, "/Applications/Codex.app/")
}

// syncChatGPTConfig keeps ChatGPT's configuration separate from the gateway
// configuration Codex CLI uses: desktop settings written by ChatGPT stay in
// ChatGPT's file, routing follows the gateway, and MCP servers whose commands
// do not exist here are dropped so their startup timeouts cannot stall every
// request. Desktop-only servers that reached the gateway file through an
// earlier shared link are removed from it; nothing else there is changed.
func syncChatGPTConfig(path, gatewayPath string) error {
	gatewayData, err := os.ReadFile(gatewayPath)
	if err != nil {
		return nil
	}
	gateway := map[string]any{}
	if toml.Unmarshal(gatewayData, &gateway) != nil {
		return nil
	}
	if dropMCPServers(gateway, desktopOnlyCommand) {
		if err := writeTOML(gatewayPath, gateway); err != nil {
			return err
		}
	}
	config := map[string]any{}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		os.Remove(path)
	}
	existing, err := os.ReadFile(path)
	if err == nil {
		if toml.Unmarshal(existing, &config) != nil {
			config = map[string]any{}
		}
	} else {
		for key, value := range gateway {
			config[key] = value
		}
	}
	for _, key := range chatgptRoutingKeys {
		if value, ok := gateway[key]; ok {
			config[key] = value
		} else {
			delete(config, key)
		}
	}
	dropMCPServers(config, func(command string) bool {
		if desktopOnlyCommand(command) {
			return true
		}
		if !filepath.IsAbs(command) {
			return false
		}
		_, err := os.Stat(command)
		return err != nil
	})
	updated, err := toml.Marshal(config)
	if err != nil {
		return err
	}
	if bytes.Equal(updated, existing) {
		return nil
	}
	return writeFileAtomic(path, updated)
}

func dropMCPServers(config map[string]any, drop func(string) bool) bool {
	servers, _ := config["mcp_servers"].(map[string]any)
	changed := false
	for name, value := range servers {
		server, _ := value.(map[string]any)
		if command, _ := server["command"].(string); command != "" && drop(command) {
			delete(servers, name)
			changed = true
		}
	}
	if changed && len(servers) == 0 {
		delete(config, "mcp_servers")
	}
	return changed
}

func writeTOML(path string, value map[string]any) error {
	data, err := toml.Marshal(value)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func writeFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
