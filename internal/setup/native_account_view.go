package setup

import (
	"encoding/base64"
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type NativeAccountIdentity struct {
	Provider     string `json:"provider"`
	Email        string `json:"email,omitempty"`
	Subscription string `json:"subscription,omitempty"`
	AuthType     string `json:"auth_type"`
}
type NativeAccountView struct {
	Agent    string                  `json:"agent"`
	Location string                  `json:"location"`
	Status   string                  `json:"status"`
	Accounts []NativeAccountIdentity `json:"accounts"`
}

func publicEmail(value any) string {
	email := strings.TrimSpace(stringValue(value))
	if len(email) > 254 || !strings.Contains(email, "@") {
		return ""
	}
	for _, c := range email {
		if unicode.IsControl(c) || unicode.IsSpace(c) {
			return ""
		}
	}
	return email
}

// Tokens are parsed only to display the native client's cached account metadata,
// never to authorize a request or infer live quota or subscription validity.
func nativeJWTMetadata(token string) map[string]any {
	if len(token) > 32768 {
		return nil
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	return jsonObject(data)
}
func codexAccountMetadata(auth map[string]any) NativeAccountIdentity {
	identity := NativeAccountIdentity{Provider: "openai", Subscription: "ChatGPT", AuthType: "oauth"}
	tokens, _ := auth["tokens"].(map[string]any)
	for _, key := range []string{"id_token", "access_token"} {
		claims := nativeJWTMetadata(stringValue(tokens[key]))
		profile, _ := claims["https://api.openai.com/profile"].(map[string]any)
		if identity.Email == "" {
			identity.Email = publicEmail(claims["email"])
			if identity.Email == "" {
				identity.Email = publicEmail(profile["email"])
			}
		}
		details, _ := claims["https://api.openai.com/auth"].(map[string]any)
		if plan := map[string]string{"free": "Free", "plus": "Plus", "pro": "Pro", "team": "Team", "business": "Business", "enterprise": "Enterprise", "edu": "Edu"}[stringValue(details["chatgpt_plan_type"])]; plan != "" && identity.Subscription == "ChatGPT" {
			identity.Subscription = "ChatGPT " + plan
		}
	}
	return identity
}
func NativeAccountFromFiles(agent string, files map[string]string) NativeAccountView {
	return nativeAccountView(agent, nativeEnvelopeReader(files))
}
func NativeCloudAccount(home, agent string) (NativeAccountView, error) {
	view := NativeAccountView{Agent: agent, Location: "cloud", Status: "unavailable", Accounts: []NativeAccountIdentity{}}
	root, _, err := NativeAccountRoot(home, agent)
	if err != nil {
		return view, err
	}
	var readErr error
	view = nativeAccountView(agent, func(name string) []byte {
		data, err := readNativeFile(filepath.Join(root, name))
		if err != nil && !os.IsNotExist(err) {
			readErr = err
		}
		return data
	})
	if readErr != nil {
		view.Status = "unavailable"
	}
	view.Location = "cloud"
	return view, nil
}
func nativeAccountView(agent string, read func(string) []byte) NativeAccountView {
	view := NativeAccountView{Agent: agent, Status: "signed_out", Accounts: []NativeAccountIdentity{}}
	env := jsonObject(read("env.json"))
	switch agent {
	case "codex":
		auth := jsonObject(read("codex/auth.json"))
		var config map[string]any
		_ = toml.Unmarshal(read("codex/config.toml"), &config)
		if nativeOAuthIdentity(agent, read) {
			view.Accounts = append(view.Accounts, codexAccountMetadata(auth))
		} else if (stringValue(auth["OPENAI_API_KEY"]) != "" || stringValue(env["OPENAI_API_KEY"]) != "") && validateNativePortability(config) == nil && validateNativePortability(map[string]any{"base_url": env["OPENAI_BASE_URL"]}) == nil {
			view.Accounts = append(view.Accounts, NativeAccountIdentity{Provider: "openai", AuthType: "api_key"})
		}
	case "claude":
		auth := jsonObject(read("claude/.credentials.json"))
		oauth, _ := auth["claudeAiOauth"].(map[string]any)
		config := jsonObject(read("claude/settings.json"))
		settingsEnv, _ := config["env"].(map[string]any)
		if nativeOAuthIdentity(agent, read) {
			identity := NativeAccountIdentity{Provider: "anthropic", AuthType: "oauth", Subscription: "Claude"}
			explicit := stringValue(settingsEnv["CLAUDE_CODE_OAUTH_TOKEN"])
			if explicit == "" {
				explicit = stringValue(env["CLAUDE_CODE_OAUTH_TOKEN"])
			}
			if explicit == "" || explicit == stringValue(oauth["accessToken"]) {
				profile := jsonObject(read("claude/.claude.json"))
				account, _ := profile["oauthAccount"].(map[string]any)
				identity.Email = publicEmail(account["emailAddress"])
				if plan := map[string]string{"pro": "Pro", "max": "Max", "team": "Team", "enterprise": "Enterprise"}[stringValue(oauth["subscriptionType"])]; plan != "" {
					identity.Subscription = "Claude " + plan
				}
			}
			view.Accounts = append(view.Accounts, identity)
		} else {
			combined := map[string]any{}
			for key, value := range env {
				combined[key] = value
			}
			for key, value := range settingsEnv {
				combined[key] = value
			}
			for _, source := range []map[string]any{combined} {
				if (stringValue(source["ANTHROPIC_API_KEY"]) != "" || stringValue(source["ANTHROPIC_AUTH_TOKEN"]) != "") && validateNativePortability(map[string]any{"base_url": source["ANTHROPIC_BASE_URL"]}) == nil {
					view.Accounts = append(view.Accounts, NativeAccountIdentity{Provider: "anthropic", AuthType: "api_key"})
					break
				}
			}
		}
	case "pi":
		auth := jsonObject(read("pi/auth.json"))
		providers := nativePiProviderFiles(read)
		names := []string{}
		for provider := range providers {
			names = append(names, provider)
		}
		sort.Strings(names)
		for _, provider := range names {
			if len(view.Accounts) >= 32 || len(provider) > 64 {
				break
			}
			identity := NativeAccountIdentity{Provider: provider, AuthType: "api_key"}
			entry, _ := auth[provider].(map[string]any)
			if stringValue(entry["access"]) != "" && stringValue(entry["type"]) != "api_key" {
				identity.AuthType = "oauth"
				if provider == "openai-codex" {
					identity = codexAccountMetadata(map[string]any{"tokens": map[string]any{"access_token": entry["access"]}})
					identity.Provider = provider
				}
			}
			view.Accounts = append(view.Accounts, identity)
		}
	case "kimi":
		view.Accounts = kimiAccounts(read)
	}
	if len(view.Accounts) > 0 {
		view.Status = "signed_in"
	}
	return view
}
