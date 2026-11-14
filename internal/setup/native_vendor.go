package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const cursorAuthFile = "cursor/auth.json"
const cursorConfigFile = "cursor/cli-config.json"
const grokAuthFile = "grok/auth.json"
const grokConfigFile = "grok/config.toml"

func cursorEntry(value map[string]any) nativeOAuthEntry {
	expires, _ := nativeJWTMetadata(stringValue(value["accessToken"]))["exp"].(float64)
	return nativeOAuthEntry{access: stringValue(value["accessToken"]), refresh: stringValue(value["refreshToken"]), key: stringValue(value["apiKey"]), expires: int64(expires) * 1000, fresh: int64(expires) * 1000}
}

func grokEntry(value map[string]any) nativeOAuthEntry {
	expires, _ := time.Parse(time.RFC3339Nano, stringValue(value["expires_at"]))
	var expiry int64
	if !expires.IsZero() {
		expiry = expires.UnixMilli()
	}
	return nativeOAuthEntry{access: stringValue(value["key"]), refresh: stringValue(value["refresh_token"]), account: stringValue(value["user_id"]), expires: expiry, fresh: expiry}
}

func (s NativeSource) cursorConfigDirectory() string {
	if strings.TrimSpace(s.Env("CURSOR_CONFIG_DIR")) != "" {
		return s.dir("CURSOR_CONFIG_DIR", ".cursor")
	}
	if strings.TrimSpace(s.Env("XDG_CONFIG_HOME")) != "" {
		return filepath.Join(s.dir("XDG_CONFIG_HOME", ".config"), "cursor")
	}
	return filepath.Join(s.Home, ".cursor")
}

// Cursor stores three individual secrets in the Mac Keychain. The cloud CLI
// reads the same fields from XDG_CONFIG_HOME/cursor/auth.json instead.
func (s NativeSource) cursorCredentials(ctx context.Context) ([]byte, error) {
	auth := map[string]any{}
	if s.Keychain != nil && s.Env("AGENT_CLI_CREDENTIAL_STORE") != "file" {
		for field, suffix := range map[string]string{"accessToken": "access-token", "refreshToken": "refresh-token", "apiKey": "api-key"} {
			data, err := s.Keychain(ctx, "cursor-"+suffix, "cursor-user")
			if err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			if len(data) > 0 {
				auth[field] = strings.TrimSpace(string(data))
			}
		}
	}
	if len(auth) == 0 {
		data, err := readNativeFile(filepath.Join(s.Home, ".cursor/auth.json"))
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &auth); err != nil {
			return nil, err
		}
	}
	out := map[string]any{}
	for _, key := range []string{"accessToken", "refreshToken", "apiKey"} {
		if value := stringValue(auth[key]); value != "" {
			out[key] = value
		}
	}
	return json.Marshal(out)
}

func grokCloudConfig(data []byte) ([]byte, error) {
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	out := map[string]any{}
	// Exclude command-based auth, MCP, hooks, plugins and machine-local paths.
	for _, key := range []string{"models", "effort"} {
		if value, ok := cfg[key]; ok {
			out[key] = value
		}
	}
	models := map[string]any{}
	entries, _ := cfg["model"].(map[string]any)
	for name, value := range entries {
		entry, _ := value.(map[string]any)
		selected := map[string]any{}
		for _, key := range []string{"model", "base_url", "name", "env_key", "api_key"} {
			if value, ok := entry[key]; ok {
				selected[key] = value
			}
		}
		if validateNativePortability(selected) == nil {
			models[name] = selected
		}
	}
	if len(models) > 0 {
		out["model"] = models
	}
	return toml.Marshal(out)
}

func vendorAccounts(agent string, read func(string) []byte) []NativeAccountIdentity {
	env := jsonObject(read("env.json"))
	if agent == "cursor" {
		auth := jsonObject(read(cursorAuthFile))
		if stringValue(auth["apiKey"]) != "" || stringValue(env["CURSOR_API_KEY"]) != "" {
			return []NativeAccountIdentity{{Provider: "cursor", AuthType: "api_key"}}
		}
		if token := stringValue(auth["accessToken"]); token != "" {
			return []NativeAccountIdentity{{Provider: "cursor", AuthType: "oauth", Email: publicEmail(nativeJWTMetadata(token)["email"])}}
		}
	} else {
		if stringValue(env["XAI_API_KEY"]) != "" {
			return []NativeAccountIdentity{{Provider: "xai", AuthType: "api_key"}}
		}
		for _, value := range jsonObject(read(grokAuthFile)) {
			entry, _ := value.(map[string]any)
			if stringValue(entry["key"]) == "" {
				continue
			}
			typeName := "oauth"
			if stringValue(entry["auth_mode"]) == "api_key" {
				typeName = "api_key"
			}
			return []NativeAccountIdentity{{Provider: "xai", AuthType: typeName, Email: publicEmail(entry["email"])}}
		}
	}
	return nil
}
