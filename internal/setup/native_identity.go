package setup

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/pelletier/go-toml/v2"
)

// The native identity is authoritative only with a saved OAuth login or an
// explicit native OAuth token. Never reinterpret a proxy key as a vendor key.
func nativeOAuthIdentity(agent string, read func(string) []byte) bool {
	switch agent {
	case "codex":
		auth := jsonObject(read("codex/auth.json"))
		tokens, _ := auth["tokens"].(map[string]any)
		mode := stringValue(auth["auth_mode"])
		return stringValue(tokens["access_token"]) != "" && (mode == "chatgpt" || (mode == "" && stringValue(auth["OPENAI_API_KEY"]) == ""))
	case "claude":
		auth := jsonObject(read("claude/.credentials.json"))
		oauth, _ := auth["claudeAiOauth"].(map[string]any)
		settings := jsonObject(read("claude/settings.json"))
		env, _ := settings["env"].(map[string]any)
		return stringValue(oauth["accessToken"]) != "" || stringValue(jsonObject(read("env.json"))["CLAUDE_CODE_OAUTH_TOKEN"]) != "" || stringValue(env["CLAUDE_CODE_OAUTH_TOKEN"]) != ""
	}
	return false
}
func nativeEnvelopeReader(files map[string]string, environment ...map[string]string) func(string) []byte {
	return func(name string) []byte {
		if name == "env.json" && len(environment) > 0 {
			data, _ := json.Marshal(environment[0])
			return data
		}
		data, _ := base64.StdEncoding.DecodeString(files[name])
		return data
	}
}
func nativeOAuthOverride(agent, key string) bool {
	switch agent {
	case "codex":
		return key == "OPENAI_BASE_URL" || key == "OPENAI_API_KEY" || key == "AZURE_OPENAI_API_KEY"
	case "claude":
		return key == "ANTHROPIC_BASE_URL" || key == "ANTHROPIC_API_KEY" || key == "ANTHROPIC_AUTH_TOKEN"
	}
	return false
}
func sanitizeNativeIdentityFile(name string, data []byte, files map[string]string, environment ...map[string]string) ([]byte, error) {
	read := nativeEnvelopeReader(files, environment...)
	if name == "codex/config.toml" && nativeOAuthIdentity("codex", read) {
		var cfg map[string]any
		if toml.Unmarshal(data, &cfg) != nil {
			return nil, fmt.Errorf("invalid Codex configuration")
		}
		delete(cfg, "model_provider")
		delete(cfg, "model_providers")
		return toml.Marshal(cfg)
	}
	if name == "claude/settings.json" {
		var cfg map[string]any
		if json.Unmarshal(data, &cfg) != nil || cfg == nil {
			return nil, fmt.Errorf("invalid Claude configuration")
		}
		env, _ := cfg["env"].(map[string]any)
		if !nativeOAuthIdentity("claude", read) && stringValue(env["CLAUDE_CODE_OAUTH_TOKEN"]) == "" {
			return data, nil
		}
		for key := range env {
			if nativeOAuthOverride("claude", key) {
				delete(env, key)
			}
		}
		return json.Marshal(cfg)
	}
	return data, nil
}

func nativeInheritedModelOverride(key string) bool {
	switch key {
	case "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL", "OPENAI_MODEL":
		return true
	}
	return false
}
