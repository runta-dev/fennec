package setup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
)

// A nil payload leaves cloud defaults alone (e.g. a model-only RPC from an
// older client). A present, empty payload resets defaults removed on the Mac.
// Only portable reasoning preferences cross this boundary, never commands,
// credentials, model routing, or project/session overrides.
type AgentReasoning struct {
	Settings map[string]any `json:"settings"`
}

type reasoningSpec struct {
	file, env, dir string
	paths          [][]string
}

var reasoningSpecs = map[string]reasoningSpec{
	"codex":    {"codex/config.toml", "CODEX_HOME", ".codex", [][]string{{"model_reasoning_effort"}}},
	"claude":   {"claude/settings.json", "CLAUDE_CONFIG_DIR", ".claude", [][]string{{"effortLevel"}, {"alwaysThinkingEnabled"}, {"modelSettings", "*", "effortLevel"}, {"env", "CLAUDE_CODE_EFFORT_LEVEL"}, {"env", "MAX_THINKING_TOKENS"}, {"env", "CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING"}}},
	"pi":       {"pi/settings.json", "PI_CODING_AGENT_DIR", ".pi/agent", [][]string{{"defaultThinkingLevel"}}},
	"kimi":     {kimiConfigFile, kimiHomeEnv, kimiHomeDefault, [][]string{{"thinking", "enabled"}, {"thinking", "effort"}}},
	"grok":     {grokConfigFile, "GROK_HOME", ".grok", [][]string{{"models", "default_reasoning_effort"}, {"effort"}}},
	"opencode": {"opencode/config/opencode/opencode.json", "XDG_CONFIG_HOME", ".config", [][]string{{"agent", "*", "variant"}, {"agent", "*", "reasoningEffort"}, {"agent", "*", "thinking", "type"}, {"agent", "*", "thinking", "budgetTokens"}}},
}

// copyReasoningPath changes only the owned leaves, preserving sibling fields.
// It also removes stale owned leaves when a local default was cleared.
func copyReasoningPath(dst, src map[string]any, path []string) {
	key := path[0]
	if key == "*" {
		keys := map[string]bool{}
		for k := range dst {
			keys[k] = true
		}
		for k := range src {
			keys[k] = true
		}
		for k := range keys {
			copyReasoningPath(dst, src, append([]string{k}, path[1:]...))
		}
		return
	}
	if len(path) == 1 {
		if value, ok := src[key]; ok {
			dst[key] = value
		} else {
			delete(dst, key)
		}
		return
	}
	child, _ := dst[key].(map[string]any)
	source, _ := src[key].(map[string]any)
	if child == nil {
		child = map[string]any{}
	}
	copyReasoningPath(child, source, path[1:])
	if len(child) > 0 {
		dst[key] = child
	} else {
		delete(dst, key)
	}
}
func reasoningSettings(agent string, cfg map[string]any) map[string]any {
	out := map[string]any{}
	for _, p := range reasoningSpecs[agent].paths {
		copyReasoningPath(out, cfg, p)
	}
	return out
}
func decodeReasoningConfig(name string, data []byte) (map[string]any, error) {
	cfg := map[string]any{}
	var err error
	if strings.HasSuffix(name, ".toml") {
		err = toml.Unmarshal(data, &cfg)
	} else {
		var plain []byte
		plain, err = hujson.Standardize(data)
		if err == nil {
			err = json.Unmarshal(plain, &cfg)
		}
	}
	if cfg == nil && err == nil {
		err = fmt.Errorf("invalid reasoning configuration")
	}
	return cfg, err
}
func encodeReasoningConfig(name string, cfg map[string]any) ([]byte, error) {
	if strings.HasSuffix(name, ".toml") {
		return toml.Marshal(cfg)
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// WithAgentReasoning uses the same filtered source snapshot as native sync;
// in particular Codex's selected profile has already been resolved here.
func WithAgentReasoning(routes []AgentRoute, native ModelAccountsRequest) ([]AgentRoute, error) {
	out := append([]AgentRoute(nil), routes...)
	for i := range out {
		spec, ok := reasoningSpecs[out[i].Agent]
		if !ok {
			continue
		}
		encoded, ok := native.Files[spec.file]
		if !ok {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("invalid reasoning source for %s", out[i].Agent)
		}
		cfg, err := decodeReasoningConfig(spec.file, raw)
		if err != nil {
			return nil, fmt.Errorf("invalid reasoning source for %s", out[i].Agent)
		}
		r := &AgentReasoning{Settings: reasoningSettings(out[i].Agent, cfg)}
		if err := validateAgentReasoning(out[i].Agent, r); err != nil {
			return nil, err
		}
		out[i].Reasoning = r
	}
	return out, nil
}
func validateAgentReasoning(agent string, r *AgentReasoning) error {
	if r == nil {
		return nil
	}
	if _, ok := reasoningSpecs[agent]; !ok {
		return fmt.Errorf("unsupported reasoning agent")
	}
	b, err := json.Marshal(r.Settings)
	if err != nil || len(b) > 16384 {
		return fmt.Errorf("invalid reasoning settings")
	}
	if r.Settings == nil {
		return nil
	}
	if !reflect.DeepEqual(r.Settings, reasoningSettings(agent, r.Settings)) {
		return fmt.Errorf("unsupported reasoning setting")
	}
	var valid func(any) bool
	valid = func(v any) bool {
		switch x := v.(type) {
		case string:
			return len(x) <= 128 && !strings.ContainsAny(x, "\x00\r\n")
		case bool:
			return true
		case float64:
			return x >= 0 && x <= 10000000
		case int64:
			return x >= 0 && x <= 10000000
		case map[string]any:
			for k, c := range x {
				if len(k) > 256 || strings.ContainsAny(k, "\x00\r\n") || !valid(c) {
					return false
				}
			}
			return true
		}
		return false
	}
	if !valid(r.Settings) {
		return fmt.Errorf("invalid reasoning value")
	}
	return nil
}

func mergeReasoning(agent string, dst, src map[string]any) {
	for _, p := range reasoningSpecs[agent].paths {
		copyReasoningPath(dst, src, p)
	}
	// A cloud default profile must not silently override the synced top-level
	// default. Explicit CLI --profile/-c overrides remain agent-owned.
	if agent == "codex" {
		if name, ok := dst["profile"].(string); ok {
			profiles, _ := dst["profiles"].(map[string]any)
			profile, _ := profiles[name].(map[string]any)
			if profile != nil {
				copyReasoningPath(profile, src, []string{"model_reasoning_effort"})
			}
		}
	}
}
func applyRouteReasoning(ctx context.Context, home string, route AgentRoute) error {
	if route.Reasoning == nil {
		return nil
	}
	if err := validateAgentReasoning(route.Agent, route.Reasoning); err != nil {
		return err
	}
	unlock, err := lockMagpieConfiguration(ctx, home)
	if err != nil {
		return err
	}
	defer unlock()
	if model, ok := readConfiguredMagpieModel(home, route.Agent); !ok || !configuredModelMatches(route.Agent, model, route.Model) {
		return fmt.Errorf("route changed before reasoning synchronization")
	}
	spec := reasoningSpecs[route.Agent]
	path := filepath.Join(resolveAgentConfigDir(home, spec.env, spec.dir), filepath.Base(spec.file))
	var cfg map[string]any
	if route.Agent == "opencode" {
		var ok bool
		path, _, cfg, ok = openCodeMagpieConfig(home)
		if !ok {
			return fmt.Errorf("invalid OpenCode configuration")
		}
	}
	previous, err := readNativeFile(path)
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg, err = decodeReasoningConfig(path, previous)
		if err != nil {
			return err
		}
	}
	before, _ := json.Marshal(cfg)
	mergeReasoning(route.Agent, cfg, route.Reasoning.Settings)
	after, _ := json.Marshal(cfg)
	if bytes.Equal(before, after) {
		return nil
	}
	updated, err := encodeReasoningConfig(path, cfg)
	if err != nil {
		return err
	}
	if route.Agent == "opencode" {
		doc, parseErr := hujson.Parse(previous)
		if parseErr != nil {
			return parseErr
		}
		patch := map[string]any{"op": "add", "path": "/agent", "value": cfg["agent"]}
		if _, exists := cfg["agent"]; !exists {
			var old map[string]any
			_ = json.Unmarshal(before, &old)
			if _, exists = old["agent"]; !exists {
				return nil
			}
			patch = map[string]any{"op": "remove", "path": "/agent"}
		}
		changes, _ := json.Marshal([]map[string]any{patch})
		if err := doc.Patch(changes); err != nil {
			return err
		}
		updated = doc.Pack()
	}
	if err := writeAtomicAgentFile(path, updated); err != nil {
		return err
	}
	readback, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	check, err := decodeReasoningConfig(path, readback)
	if err != nil {
		return err
	}
	want, _ := json.Marshal(reasoningSettings(route.Agent, cfg))
	got, _ := json.Marshal(reasoningSettings(route.Agent, check))
	if !bytes.Equal(want, got) {
		return fmt.Errorf("reasoning readback mismatch")
	}
	return nil
}

// Native conflict resolution owns model selection separately from portable
// defaults. Local reasoning changes (including removal) must survive a merge.
func syncNativeReasoning(name string, merged, local []byte) ([]byte, error) {
	for agent, spec := range reasoningSpecs {
		if spec.file != name {
			continue
		}
		dst, err := decodeReasoningConfig(name, merged)
		if err != nil {
			return nil, err
		}
		src, err := decodeReasoningConfig(name, local)
		if err != nil {
			return nil, err
		}
		mergeReasoning(agent, dst, src)
		return encodeReasoningConfig(name, dst)
	}
	return merged, nil
}
