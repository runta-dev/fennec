package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/text/unicode/norm"
)

// Native files are private to Fennec's sandbox agent configuration. No sessions,
// shell hooks, MCP commands, or arbitrary local directories are copied.
var nativeFiles = map[string]bool{
	"codex/auth.json": true, "codex/config.toml": true,
	"claude/.credentials.json": true, "claude/settings.json": true, "claude/.claude.json": true,
	"pi/auth.json": true, "pi/settings.json": true, "pi/models.json": true,
	"opencode/data/opencode/auth.json": true, "opencode/config/opencode/opencode.json": true,
	"env.json": true,
}
var nativeEnvKeys = []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "GEMINI_API_KEY", "GOOGLE_API_KEY", "GROQ_API_KEY", "OPENROUTER_API_KEY", "XAI_API_KEY", "MISTRAL_API_KEY", "AZURE_OPENAI_API_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION"}

type NativeSource struct {
	Gateway  bool
	Home     string
	Env      func(string) string
	Keychain func(context.Context, string, string) ([]byte, error)
}

func (s NativeSource) dir(key, fallback string) string {
	value := s.Env(key)
	if value == "" {
		return filepath.Join(s.Home, fallback)
	}
	if strings.HasPrefix(value, "~/") {
		value = filepath.Join(s.Home, value[2:])
	}
	return filepath.Clean(value)
}
func readNativeFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, fmt.Errorf("invalid native credential/configuration file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return nil, fmt.Errorf("cannot read native configuration")
	}
	return data, nil
}
func readNativeKeychain(ctx context.Context, service, account string) ([]byte, error) {
	if runtime.GOOS != "darwin" {
		return nil, os.ErrNotExist
	}
	step, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	args := []string{"find-generic-password", "-s", service, "-w"}
	if account != "" {
		args = append(args, "-a", account)
	}
	data, err := exec.CommandContext(step, "/usr/bin/security", args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 44 {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("allow access to the agent's macOS Keychain credential to continue")
	}
	data = bytes.TrimSpace(data)
	if !json.Valid(data) {
		decoded, e := hex.DecodeString(string(data))
		if e != nil || !json.Valid(decoded) {
			return nil, fmt.Errorf("invalid native Keychain credential")
		}
		data = decoded
	}
	return data, nil
}
func GatherNativeAccounts(ctx context.Context, home string, agents []string, gateway ...bool) (ModelAccountsRequest, error) {
	env := nativeSourceEnvironment(ctx)
	return (NativeSource{Home: home, Gateway: len(gateway) > 0 && gateway[0], Env: func(k string) string { return env[k] }, Keychain: readNativeKeychain}).GatherAll(ctx, agents)
}
func (s NativeSource) GatherAll(ctx context.Context, agents []string) (ModelAccountsRequest, error) {
	if err := validateAgentInventory(agents); err != nil {
		return ModelAccountsRequest{}, err
	}
	result := ModelAccountsRequest{Version: 1, Native: true, DeferCatalog: true, Agents: agents, Files: map[string]string{}, NativeProblems: map[string]string{}}
	allEnv := map[string]string{}
	for _, id := range agents {
		one, err := s.Gather(ctx, []string{id})
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if err != nil {
			result.NativeProblems[id] = "credentials_unavailable"
			var configuration *nativeConfigurationError
			if errors.As(err, &configuration) {
				result.NativeProblems[id] = configuration.problem
			}
			continue
		}
		for name, data := range one.Files {
			if name == "env.json" {
				raw, _ := base64.StdEncoding.DecodeString(data)
				var env map[string]string
				_ = json.Unmarshal(raw, &env)
				for key, value := range env {
					allEnv[key] = value
				}
				continue
			}
			result.Files[name] = data
		}
	}
	raw, _ := json.Marshal(allEnv)
	result.Files["env.json"] = base64.StdEncoding.EncodeToString(raw)
	return result, nil
}
func (s NativeSource) Gather(ctx context.Context, agents []string) (ModelAccountsRequest, error) {
	r := ModelAccountsRequest{Version: 1, Native: true, DeferCatalog: true, Agents: agents, Files: map[string]string{}}
	if err := validateAgentInventory(agents); err != nil {
		return r, err
	}
	if s.Env == nil {
		s.Env = func(string) string { return "" }
	}
	env := map[string]string{}
	add := func(name string, b []byte) error {
		if !nativeFiles[name] {
			return fmt.Errorf("unsupported native file")
		}
		if !strings.HasSuffix(name, ".toml") && !json.Valid(b) {
			return fmt.Errorf("invalid native configuration for %s", strings.Split(name, "/")[0])
		}
		if !s.Gateway {
			var err error
			b, err = sanitizeNativeIdentityFile(name, b, r.Files, env)
			if err != nil {
				return err
			}
			b, err = sanitizeNativeModelFile(name, b)
			if err != nil {
				return err
			}
		}
		var value any
		if strings.HasSuffix(name, ".toml") {
			var m map[string]any
			if toml.Unmarshal(b, &m) != nil {
				return fmt.Errorf("invalid native TOML")
			}
			value = m
		} else {
			_ = json.Unmarshal(b, &value)
		}
		if err := validateNativePortability(value); err != nil {
			var configuration *nativeConfigurationError
			if !s.Gateway || !errors.As(err, &configuration) || configuration.problem != "local_provider_unavailable" {
				return err
			}
		}
		r.Files[name] = base64.StdEncoding.EncodeToString(b)
		return nil
	}
	read := func(name, path string, keys []string) error {
		b, e := readNativeFile(path)
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return fmt.Errorf("cannot read native %s configuration", strings.Split(name, "/")[0])
		}
		if keys != nil {
			var src map[string]json.RawMessage
			if json.Unmarshal(b, &src) != nil {
				return fmt.Errorf("invalid native configuration")
			}
			dst := map[string]json.RawMessage{}
			for _, k := range keys {
				if v, ok := src[k]; ok {
					dst[k] = v
				}
			}
			b, _ = json.Marshal(dst)
		}
		if name == "claude/settings.json" {
			var cfg map[string]json.RawMessage
			_ = json.Unmarshal(b, &cfg)
			var source map[string]string
			_ = json.Unmarshal(cfg["env"], &source)
			safe := map[string]string{}
			for _, key := range nativeEnvKeys {
				if value := source[key]; value != "" {
					safe[key] = value
				}
			}
			cfg["env"], _ = json.Marshal(safe)
			b, _ = json.Marshal(cfg)
		}
		return add(name, b)
	}
	for _, k := range nativeEnvKeys {
		needed := false
		for _, id := range agents {
			if nativeEnvForAgent(id, k) {
				needed = true
			}
		}
		if !needed {
			continue
		}
		if v := s.Env(k); v != "" {
			env[k] = v
		}
	}
	// Each provider remains configured by its original agent. No gateway model
	// selection or inference probe is needed for native authentication.
	for _, agent := range agents {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		switch agent {
		case "codex":
			dir := s.dir("CODEX_HOME", ".codex")
			config := map[string]any{}
			b, e := readNativeFile(filepath.Join(dir, "config.toml"))
			if e == nil {
				if toml.Unmarshal(b, &config) != nil {
					return r, fmt.Errorf("invalid Codex configuration")
				}
			} else if !os.IsNotExist(e) {
				return r, e
			}
			mode, _ := config["cli_auth_credentials_store"].(string)
			var auth []byte
			if (mode == "keyring" || mode == "auto") && s.Keychain != nil {
				canonical, e := filepath.EvalSymlinks(dir)
				if e != nil {
					canonical = dir
				}
				sum := sha256.Sum256([]byte(canonical))
				auth, e = s.Keychain(ctx, "Codex Auth", "cli|"+hex.EncodeToString(sum[:])[:16])
				if e != nil && !os.IsNotExist(e) {
					return r, e
				}
				if mode == "keyring" && e != nil {
					return r, fmt.Errorf("Codex Keychain sign-in was not found")
				}
			}
			if len(auth) == 0 && mode != "ephemeral" {
				auth, e = readNativeFile(filepath.Join(dir, "auth.json"))
				if e != nil && !os.IsNotExist(e) {
					return r, e
				}
			}
			if len(auth) > 0 {
				if e := add("codex/auth.json", auth); e != nil {
					return r, e
				}
			}
			if len(auth) == 0 && env["OPENAI_API_KEY"] != "" {
				b, _ := json.Marshal(map[string]string{"auth_mode": "apikey", "OPENAI_API_KEY": env["OPENAI_API_KEY"]})
				if e := add("codex/auth.json", b); e != nil {
					return r, e
				}
			}
			selected := map[string]any{"cli_auth_credentials_store": "file"}
			if name, ok := config["profile"].(string); ok {
				profiles, _ := config["profiles"].(map[string]any)
				profile, _ := profiles[name].(map[string]any)
				for _, k := range []string{"model", "model_provider", "model_reasoning_effort", "model_reasoning_summary", "model_verbosity"} {
					if v, ok := profile[k]; ok {
						config[k] = v
					}
				}
			}
			for _, k := range []string{"model", "model_provider", "model_providers", "model_reasoning_effort", "model_reasoning_summary", "model_verbosity"} {
				if v, ok := config[k]; ok {
					selected[k] = v
				}
			}
			if providers, ok := selected["model_providers"].(map[string]any); ok {
				chosen, _ := selected["model_provider"].(string)
				if chosen != "" && providers[chosen] != nil {
					selected["model_providers"] = map[string]any{chosen: providers[chosen]}
				} else {
					delete(selected, "model_providers")
				}
			}
			b, e = toml.Marshal(selected)
			if e != nil {
				return r, fmt.Errorf("cannot encode Codex configuration")
			}
			if e = add("codex/config.toml", b); e != nil {
				return r, e
			}
		case "claude":
			dir := s.dir("CLAUDE_CONFIG_DIR", ".claude")
			var b []byte
			var e error
			if s.Keychain != nil {
				service := "Claude Code-credentials"
				if s.Env("CLAUDE_CONFIG_DIR") != "" {
					sum := sha256.Sum256([]byte(norm.NFC.String(dir)))
					service += "-" + hex.EncodeToString(sum[:])[:8]
				}
				b, e = s.Keychain(ctx, service, nativeKeychainAccount(s.Env("USER")))
				if e != nil && !os.IsNotExist(e) {
					return r, e
				}
			}
			if len(b) == 0 {
				b, e = readNativeFile(filepath.Join(dir, ".credentials.json"))
				if e != nil && !os.IsNotExist(e) {
					return r, e
				}
			}
			if len(b) > 0 {
				if e := add("claude/.credentials.json", b); e != nil {
					return r, e
				}
			}
			if e := read("claude/settings.json", filepath.Join(dir, "settings.json"), []string{"model", "env"}); e != nil {
				return r, e
			}
			cfg := map[string]any{}
			settings, _ := base64.StdEncoding.DecodeString(r.Files["claude/settings.json"])
			_ = json.Unmarshal(settings, &cfg)
			values, _ := cfg["env"].(map[string]any)
			if values == nil {
				values = map[string]any{}
			}
			for key, value := range env {
				if nativeEnvForAgent("claude", key) {
					values[key] = value
				}
			}
			cfg["env"] = values
			settings, _ = json.Marshal(cfg)
			if e := add("claude/settings.json", settings); e != nil {
				return r, e
			}
			profile := filepath.Join(dir, ".claude.json")
			if s.Env("CLAUDE_CONFIG_DIR") == "" {
				profile = filepath.Join(s.Home, ".claude.json")
			}
			if e := read("claude/.claude.json", profile, []string{"oauthAccount", "hasCompletedOnboarding"}); e != nil {
				return r, e
			}
			if len(b) > 0 || env["ANTHROPIC_API_KEY"] != "" || env["ANTHROPIC_AUTH_TOKEN"] != "" || nativeOAuthIdentity("claude", nativeEnvelopeReader(r.Files, env)) {
				p := map[string]any{}
				raw, _ := base64.StdEncoding.DecodeString(r.Files["claude/.claude.json"])
				_ = json.Unmarshal(raw, &p)
				p["hasCompletedOnboarding"] = true
				raw, _ = json.Marshal(p)
				if e := add("claude/.claude.json", raw); e != nil {
					return r, e
				}
			}
		case "pi":
			dir := s.dir("PI_CODING_AGENT_DIR", ".pi/agent")
			for _, entry := range []struct {
				name string
				keys []string
			}{{"auth.json", nil}, {"settings.json", []string{"defaultProvider", "defaultModel", "defaultThinkingLevel"}}, {"models.json", nil}} {
				if e := read("pi/"+entry.name, filepath.Join(dir, entry.name), entry.keys); e != nil {
					return r, e
				}
			}
		case "opencode":
			if e := read("opencode/data/opencode/auth.json", filepath.Join(s.dir("XDG_DATA_HOME", ".local/share"), "opencode/auth.json"), nil); e != nil {
				return r, e
			}
			if e := read("opencode/config/opencode/opencode.json", filepath.Join(s.dir("XDG_CONFIG_HOME", ".config"), "opencode/opencode.json"), []string{"model", "small_model", "provider"}); e != nil {
				return r, e
			}
		}
	}
	if !s.Gateway {
		for _, id := range agents {
			if nativeOAuthIdentity(id, nativeEnvelopeReader(r.Files, env)) {
				for key := range env {
					if nativeOAuthOverride(id, key) {
						delete(env, key)
					}
				}
			}
		}
	}
	b, _ := json.Marshal(env)
	if e := add("env.json", b); e != nil {
		return r, e
	}
	return r, nil
}

func ApplyNativeAccounts(ctx context.Context, home string, r ModelAccountsRequest) (ModelAccountsResult, error) {
	result := ModelAccountsResult{Version: 1, Migrated: len(r.Files), Timings: map[string]float64{}, NativeReady: map[string]bool{}}
	if err := ValidateModelAccountsRequest(r); err != nil {
		return result, err
	}
	root := filepath.Join(home, ".local/fennec/native")
	if err := ensureNativeDirectory(home, ".local/fennec/native"); err != nil {
		return result, err
	}
	lock, e := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return result, e
	}
	defer lock.Close()
	for {
		e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			break
		}
		if !errors.Is(e, syscall.EWOULDBLOCK) {
			return result, e
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	baseline := map[string]credentialPair{}
	marker := filepath.Join(root, ".baseline.json")
	if b, e := readNativeFile(marker); e == nil {
		if json.Unmarshal(b, &baseline) != nil {
			return result, fmt.Errorf("invalid native synchronization baseline")
		}
	} else if !os.IsNotExist(e) {
		return result, e
	}
	writes := []credentialWrite{}
	for name, encoded := range r.Files {
		if !nativeFiles[name] {
			return result, fmt.Errorf("unsupported native credential file")
		}
		data, e := base64.StdEncoding.DecodeString(encoded)
		if e != nil || len(data) > 4<<20 {
			return result, fmt.Errorf("invalid native credential envelope")
		}
		if strings.HasSuffix(name, ".toml") {
			var v map[string]any
			if toml.Unmarshal(data, &v) != nil {
				return result, fmt.Errorf("invalid native TOML")
			}
		} else if !json.Valid(data) {
			return result, fmt.Errorf("invalid native JSON")
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		if e := ensureNativeDirectory(home, filepath.Join(".local/fennec/native", filepath.Dir(name))); e != nil {
			return result, e
		}
		previous, e := readNativeFile(path)
		exists := e == nil
		if e != nil && !os.IsNotExist(e) {
			return result, e
		}
		data, e = sanitizeNativeIdentityFile(name, data, r.Files)
		if e != nil {
			return result, e
		}
		data, e = sanitizeNativeModelFile(name, data)
		if e != nil {
			return result, e
		}
		previousPolicy := previous
		if exists {
			previousPolicy, e = sanitizeNativeIdentityFile(name, previous, r.Files)
			if e != nil {
				return result, e
			}
		}
		source := credentialHash(data)
		old := baseline[name]
		if exists && source == old.Source && !bytes.Equal(previous, data) && name != "codex/config.toml" && name != "claude/settings.json" && name != "pi/settings.json" {
			continue
		}
		if exists && !bytes.Equal(previous, data) {
			if source == old.Source {
				data = previousPolicy // Preserve refreshes and explicit native model choices.
			} else if credentialHash(previous) != old.Remote && compatibleNativeNonModel(name, previousPolicy, data) {
				data = previousPolicy
			} else if credentialHash(previous) != old.Remote {
				return result, fmt.Errorf("native credentials changed on both computers; cloud copy preserved")
			}
		}
		if name == "pi/settings.json" {
			data, e = sanitizeNativePiSettings(data, nativePiProviderFiles(func(name string) []byte { raw, _ := base64.StdEncoding.DecodeString(r.Files[name]); return raw }))
			if e != nil {
				return result, e
			}
		}
		data, e = sanitizeNativeModelFile(name, data)
		if e != nil {
			return result, e
		}
		baseline[name] = credentialPair{Source: source, Remote: credentialHash(data)}
		if !exists || !bytes.Equal(previous, data) {
			writes = append(writes, credentialWrite{path: path, data: data, previous: previous, existed: exists})
		}

	}
	for _, w := range writes {
		if e := atomicCredential(w.path, w.data, w); e != nil {
			return result, e
		}
	}
	b, _ := json.Marshal(baseline)
	if e := atomicCredential(marker, b); e != nil {
		return result, e
	}
	for _, id := range r.Agents {
		result.NativeReady[id] = r.NativeProblems[id] == "" && nativeHasCredentials(root, id)
	}
	result.AvailableAgents = availableAgentInventory(home, r.Agents)
	if e := ConfigureAgentMode(home, true); e != nil {
		return result, e
	}
	return result, nil
}
func nativeHasCredentials(root, agent string) bool {
	env := map[string]string{}
	b, _ := os.ReadFile(filepath.Join(root, "env.json"))
	_ = json.Unmarshal(b, &env)
	file := map[string]string{"codex": "codex/auth.json", "claude": "claude/.credentials.json", "pi": "pi/auth.json", "opencode": "opencode/data/opencode/auth.json"}[agent]
	b, _ = os.ReadFile(filepath.Join(root, file))
	v := map[string]any{}
	_ = json.Unmarshal(b, &v)
	text := func(v any) bool { s, _ := v.(string); return strings.TrimSpace(s) != "" }
	switch agent {
	case "codex":
		tokens, _ := v["tokens"].(map[string]any)
		if text(tokens["access_token"]) || text(v["OPENAI_API_KEY"]) || env["OPENAI_API_KEY"] != "" {
			return true
		}
		var config map[string]any
		b, _ = os.ReadFile(filepath.Join(root, "codex/config.toml"))
		_ = toml.Unmarshal(b, &config)
		providers, _ := config["model_providers"].(map[string]any)
		name, _ := config["model_provider"].(string)
		provider, _ := providers[name].(map[string]any)
		key, _ := provider["env_key"].(string)
		return text(provider["experimental_bearer_token"]) || key != "" && env[key] != "" || provider["requires_openai_auth"] == false
	case "claude":
		tokens, _ := v["claudeAiOauth"].(map[string]any)
		if text(tokens["accessToken"]) || env["ANTHROPIC_API_KEY"] != "" || env["ANTHROPIC_AUTH_TOKEN"] != "" || env["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
			return true
		}
		var cfg struct {
			Env map[string]string `json:"env"`
		}
		b, _ := os.ReadFile(filepath.Join(root, "claude/settings.json"))
		_ = json.Unmarshal(b, &cfg)
		return cfg.Env["ANTHROPIC_API_KEY"] != "" || cfg.Env["ANTHROPIC_AUTH_TOKEN"] != "" || cfg.Env["CLAUDE_CODE_OAUTH_TOKEN"] != ""
	case "pi", "opencode":
		for _, entry := range v {
			m, _ := entry.(map[string]any)
			if text(m["key"]) || text(m["access"]) || text(m["apiKey"]) {
				return true
			}
		}
		for key, value := range env {
			if value != "" && (strings.HasSuffix(key, "API_KEY") || key == "ANTHROPIC_AUTH_TOKEN") {
				return true
			}
		}
		configPath := "pi/models.json"
		if agent == "opencode" {
			configPath = "opencode/config/opencode/opencode.json"
		}
		b, _ = os.ReadFile(filepath.Join(root, configPath))
		var config any
		_ = json.Unmarshal(b, &config)
		if nativeProviderKey(config, env) {
			return true
		}
		return false
	}
	return false
}

func nativeProviderKey(value any, env map[string]string) bool {
	switch v := value.(type) {
	case map[string]any:
		if key, ok := v["apiKey"].(string); ok && key != "" && !strings.HasPrefix(key, "!") {
			if strings.ToUpper(key) != key || strings.ContainsAny(key, "-.") || env[key] != "" {
				return true
			}
		}
		for _, item := range v {
			if nativeProviderKey(item, env) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if nativeProviderKey(item, env) {
				return true
			}
		}
	}
	return false
}

func ensureNativeDirectory(home, relative string) error {
	path := home
	for _, part := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		if part == "." {
			continue
		}
		if part == ".." || part == "" {
			return fmt.Errorf("invalid native directory")
		}
		path = filepath.Join(path, part)
		if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return fmt.Errorf("cannot create native credential directory")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("native credential directory must not be a symbolic link")
		}
	}
	return nil
}

type nativeConfigurationError struct{ problem string }

func (e *nativeConfigurationError) Error() string {
	return "native configuration requires a cloud-accessible provider"
}
func validateNativePortability(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, entry := range v {
			text, _ := entry.(string)
			switch key {
			case "base_url", "baseUrl", "baseURL", "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL":
				u, err := url.Parse(text)
				if err == nil {
					host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
					ip := net.ParseIP(host)
					if host == "localhost" || strings.HasSuffix(host, ".localhost") || ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
						return &nativeConfigurationError{"local_provider_unavailable"}
					}
				}
			case "apiKey", "key", "api_key":
				if strings.HasPrefix(text, "!") {
					return &nativeConfigurationError{"local_credential_command"}
				}
			}
			if err := validateNativePortability(entry); err != nil {
				return err
			}
		}
	case []any:
		for _, entry := range v {
			if err := validateNativePortability(entry); err != nil {
				return err
			}
		}
	}
	return nil
}

func nativeEnvForAgent(agent, key string) bool {
	switch agent {
	case "codex":
		return strings.HasPrefix(key, "OPENAI_") || strings.HasPrefix(key, "AZURE_OPENAI_")
	case "claude":
		return strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_") || strings.HasPrefix(key, "AWS_")
	case "pi", "opencode", "magpie":
		return true
	}
	return false
}

func nativeKeychainAccount(name string) string {
	if name == "" {
		if current, err := user.Current(); err == nil {
			name = current.Username
		}
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9._-]+$`).MatchString(name) {
		return "claude-code-user"
	}
	return name
}
