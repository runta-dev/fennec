package setup

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Kimi Code (the `kimi` CLI) keeps everything under one directory, ~/.kimi-code
// or $KIMI_CODE_HOME: config.toml (providers, models, default model, hooks),
// the OAuth sign-in in credentials/kimi-code.json and the account region in
// a one-word region file. The cloud copy lives in native/kimi, which the
// cloud points KIMI_CODE_HOME at.
const (
	kimiHomeEnv        = "KIMI_CODE_HOME"
	kimiHomeDefault    = ".kimi-code"
	kimiConfigFile     = "kimi/config.toml"
	kimiCredentialFile = "kimi/credentials/kimi-code.json"
	kimiRegionFile     = "kimi/region"
)

var kimiRegionPattern = regexp.MustCompile(`^[a-z]{2,8}$`)

// kimiCloudConfig keeps the parts of a Kimi config.toml that make sense on
// another computer: the default model, thinking settings, providers that
// talk to a remote API with the models that use them, and the service
// sign-in references. Hooks, MCP servers, plugins and every other key, which
// can run local commands or name local paths, stay on the Mac; so do
// providers on this Mac's loopback (a local Magpie gateway).
func kimiCloudConfig(raw []byte) ([]byte, error) {
	var config map[string]any
	if err := toml.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("invalid Kimi Code configuration")
	}
	out := map[string]any{}
	providers := map[string]any{}
	if all, ok := config["providers"].(map[string]any); ok {
		for name, value := range all {
			provider, ok := value.(map[string]any)
			if !ok || name == "magpie" {
				continue
			}
			if base, _ := provider["base_url"].(string); base != "" && kimiLoopback(base) {
				continue
			}
			providers[name] = provider
		}
	}
	if len(providers) > 0 {
		out["providers"] = providers
	}
	models := map[string]any{}
	if all, ok := config["models"].(map[string]any); ok {
		for name, value := range all {
			model, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if provider, _ := model["provider"].(string); providers[provider] == nil {
				continue
			}
			models[name] = model
		}
	}
	if len(models) > 0 {
		out["models"] = models
	}
	if name, _ := config["default_model"].(string); name != "" && models[name] != nil {
		out["default_model"] = name
	}
	for _, key := range []string{"thinking", "services"} {
		if value, ok := config[key].(map[string]any); ok {
			out[key] = value
		}
	}
	return toml.Marshal(out)
}

func kimiLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.")
}

// kimiRegion validates the region file: one short lowercase word ("cn").
func kimiRegion(raw []byte) ([]byte, bool) {
	value := strings.TrimSpace(string(raw))
	if !kimiRegionPattern.MatchString(value) {
		return nil, false
	}
	return []byte(value), true
}

// kimiCredentialKeys are the fields of Kimi's OAuth file that one sign-in
// rotates together.
var kimiCredentialKeys = []string{"access_token", "refresh_token", "expires_at", "expires_in", "scope", "token_type"}

// kimiEntry reads Kimi's OAuth file (credentials/kimi-code.json). Its
// expires_at is in Unix seconds. Kimi has no read-only account endpoint known
// here, so copies of different sign-ins are never confirmed online: same
// grant keeps the later expiry, otherwise each computer keeps its own.
func kimiEntry(v map[string]any) nativeOAuthEntry {
	expires := int64(numberValue(v["expires_at"]))
	if expires > 0 && expires < 1e12 {
		expires *= 1000
	}
	return nativeOAuthEntry{access: stringValue(v["access_token"]), refresh: stringValue(v["refresh_token"]), expires: expires, fresh: expires}
}

// kimiAccounts lists Kimi's sign-ins without secrets: the Kimi Code OAuth
// account, then each provider configured with an API key.
func kimiAccounts(read func(string) []byte) []NativeAccountIdentity {
	accounts := []NativeAccountIdentity{}
	auth := jsonObject(read(kimiCredentialFile))
	if stringValue(auth["access_token"]) != "" || stringValue(auth["refresh_token"]) != "" {
		accounts = append(accounts, NativeAccountIdentity{Provider: "kimi", AuthType: "oauth", Subscription: "Kimi Code"})
	}
	var config map[string]any
	_ = toml.Unmarshal(read(kimiConfigFile), &config)
	providers, _ := config["providers"].(map[string]any)
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		provider, _ := providers[name].(map[string]any)
		if stringValue(provider["api_key"]) == "" || len(name) > 64 || len(accounts) >= 32 {
			continue
		}
		accounts = append(accounts, NativeAccountIdentity{Provider: name, AuthType: "api_key"})
	}
	return accounts
}
