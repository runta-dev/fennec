package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReasoningReconcilesUnchangedModel(t *testing.T) {
	home := t.TempDir()
	nativeFixture(t, home, ".codex/config.toml", `model_provider = "magpie"
model = "codex/gpt-test"
model_reasoning_effort = "high"
profile = "work"
[profiles.work]
model_reasoning_effort = "xhigh"
[model_providers.magpie]
base_url = "http://127.0.0.1:3425/v1"
[tui]
notifications = false
`)
	route := AgentRoute{Agent: "codex", Model: "codex/gpt-test", Reasoning: &AgentReasoning{Settings: map[string]any{"model_reasoning_effort": "low"}}}
	// The unchanged route needs no gateway, CLI subprocess or inference call.
	results, err := ReconcileLocalRoutes(context.Background(), home, []AgentRoute{route})
	if err != nil || len(results) != 1 || results[0].Status != "configured" {
		t.Fatalf("%v %v", results, err)
	}
	path := filepath.Join(home, ".codex/config.toml")
	raw, _ := os.ReadFile(path)
	cfg, err := decodeReasoningConfig(path, raw)
	if err != nil || cfg["model_reasoning_effort"] != "low" || cfg["model"] != "codex/gpt-test" || cfg["tui"] == nil {
		t.Fatalf("%s %v", raw, err)
	}
	profile := cfg["profiles"].(map[string]any)["work"].(map[string]any)
	if profile["model_reasoning_effort"] != "low" {
		t.Fatal("profile masks synced default")
	}
	stat, _ := os.Stat(path)
	if _, err = ReconcileLocalRoutes(context.Background(), home, []AgentRoute{route}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !stat.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged sync rewrote file")
	}
	route.Reasoning = nil
	if _, err = ReconcileLocalRoutes(context.Background(), home, []AgentRoute{route}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "low") {
		t.Fatal("model-only RPC lost reasoning")
	}
	route.Reasoning = &AgentReasoning{Settings: map[string]any{}}
	if _, err = ReconcileLocalRoutes(context.Background(), home, []AgentRoute{route}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "model_reasoning_effort") {
		t.Fatal("cleared local default left stale effort")
	}
}

func TestReasoningGatherUsesProfileAndClaudeOverrides(t *testing.T) {
	home := t.TempDir()
	nativeFixture(t, home, ".codex/config.toml", `model_reasoning_effort="high"
profile="work"
[profiles.work]
model_reasoning_effort="low"
`)
	nativeFixture(t, home, ".claude/settings.json", `{"effortLevel":"medium","alwaysThinkingEnabled":false,"modelSettings":{"claude-opus-5-5":{"effortLevel":"low","command":"/Users/test/local"}},"env":{"CLAUDE_CODE_EFFORT_LEVEL":"high","MAX_THINKING_TOKENS":"2048","UNSAFE":"secret"},"hooks":{"ignored":true}}`)
	native, err := (NativeSource{Home: home, Gateway: true, Env: func(k string) string {
		if k == "CLAUDE_CODE_EFFORT_LEVEL" {
			return "low"
		}
		return ""
	}}).Gather(context.Background(), []string{"codex", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	routes, err := WithAgentReasoning([]AgentRoute{{Agent: "codex"}, {Agent: "claude"}}, native)
	if err != nil {
		t.Fatal(err)
	}
	if routes[0].Reasoning.Settings["model_reasoning_effort"] != "low" {
		t.Fatal(routes[0])
	}
	raw, _ := json.Marshal(routes[1].Reasoning)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "command") || strings.Contains(string(raw), "hooks") {
		t.Fatalf("unsafe export: %s", raw)
	}
	env := routes[1].Reasoning.Settings["env"].(map[string]any)
	if env["CLAUDE_CODE_EFFORT_LEVEL"] != "low" || env["MAX_THINKING_TOKENS"] != "2048" {
		t.Fatal(env)
	}
}

func TestNativeReasoningWinsModelConflict(t *testing.T) {
	home := t.TempDir()
	put := func(effort string) ModelAccountsRequest {
		return ModelAccountsRequest{Version: 1, Native: true, Agents: []string{"codex"}, Files: map[string]string{"codex/config.toml": base64.StdEncoding.EncodeToString([]byte("model='gpt-5'\nmodel_reasoning_effort='" + effort + "'\n"))}}
	}
	if _, err := ApplyNativeAccounts(context.Background(), home, put("medium")); err != nil {
		t.Fatal(err)
	}
	nativeFixture(t, home, ".local/fennec/native/codex/config.toml", "model='gpt-5.1'\nmodel_reasoning_effort='high'\n[tui]\nnotifications=false\n")
	if _, err := ApplyNativeAccounts(context.Background(), home, put("low")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".local/fennec/native/codex/config.toml"))
	cfg, _ := decodeReasoningConfig("x.toml", raw)
	if cfg["model"] != "gpt-5.1" || cfg["model_reasoning_effort"] != "low" || cfg["tui"] == nil {
		t.Fatalf("%s", raw)
	}
}

func TestReasoningPreservesSiblingSettings(t *testing.T) {
	cases := []struct{ agent, local, cloud, want string }{
		{"claude", `{"modelSettings":{"claude-opus-5-5":{"effortLevel":"low"}},"env":{"MAX_THINKING_TOKENS":"1024"}}`, `{"model":"claude/x","modelSettings":{"claude-opus-5-5":{"effortLevel":"high","other":true}},"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:3425","CLAUDE_CODE_EFFORT_LEVEL":"max"}}`, `"other":true`},
		{"pi", `{"defaultThinkingLevel":"low"}`, `{"defaultProvider":"magpie","defaultModel":"codex/test","defaultThinkingLevel":"high"}`, `"defaultModel":"codex/test"`},
		{"grok", `{"models":{"default_reasoning_effort":"low"}}`, `{"models":{"default":"magpie/test","default_reasoning_effort":"high"}}`, `"default":"magpie/test"`},
		{"kimi", `{"thinking":{"enabled":false}}`, `{"thinking":{"enabled":true},"default_model":"magpie/test"}`, `"default_model":"magpie/test"`},
		{"opencode", `{"agent":{"build":{"variant":"low"}}}`, `{"agent":{"build":{"variant":"high","permission":"ask"}},"model":"magpie/test"}`, `"permission":"ask"`},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			var local, cloud map[string]any
			json.Unmarshal([]byte(tc.local), &local)
			json.Unmarshal([]byte(tc.cloud), &cloud)
			r := &AgentReasoning{Settings: reasoningSettings(tc.agent, local)}
			if err := validateAgentReasoning(tc.agent, r); err != nil {
				t.Fatal(err)
			}
			mergeReasoning(tc.agent, cloud, r.Settings)
			raw, _ := json.Marshal(cloud)
			if !strings.Contains(string(raw), tc.want) || strings.Contains(string(raw), `"high"`) || strings.Contains(string(raw), `"max"`) {
				t.Fatalf("%s", raw)
			}
		})
	}
}

func TestReasoningRejectsUnownedFields(t *testing.T) {
	for _, settings := range []map[string]any{{"hooks": "bad"}, {"env": map[string]any{"ANTHROPIC_API_KEY": "secret"}}, {"effortLevel": []string{"low"}}} {
		if validateAgentReasoning("claude", &AgentReasoning{Settings: settings}) == nil {
			t.Fatal("accepted unowned or malformed setting")
		}
	}
}

func TestReasoningReadbackRejectsOldCloud(t *testing.T) {
	routes := []AgentRoute{{Agent: "codex", Model: "codex/test", Reasoning: &AgentReasoning{Settings: map[string]any{"model_reasoning_effort": "low"}}}}
	old := []byte(`[{"agent":"codex","model":"codex/test","status":"configured"}]`)
	if _, err := DecodeRouteResults(old, routes); err == nil {
		t.Fatal("accepted old cloud without reasoning confirmation")
	}
	current, _ := json.Marshal([]RouteResult{{Agent: "codex", Model: "codex/test", Status: "configured", Reasoning: routes[0].Reasoning}})
	if _, err := DecodeRouteResults(current, routes); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeReasoningJSONC(t *testing.T) {
	home := t.TempDir()
	// Keep reconciliation on the fixture even when the host sets config overrides.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	nativeFixture(t, home, ".config/opencode/opencode.jsonc", `{
 // Keep provider comments.
 "model":"magpie/codex/test",
 "provider":{"magpie":{"options":{"baseURL":"http://127.0.0.1:3425/v1","apiKey":"magpie"}}},
 "agent":{"build":{"variant":"low","prompt":"/Users/local/private"}},
 }`)
	native, err := (NativeSource{Home: home, Gateway: true}).Gather(context.Background(), []string{"opencode"})
	if err != nil {
		t.Fatal(err)
	}
	routes, err := WithAgentReasoning([]AgentRoute{{Agent: "opencode", Model: "codex/test"}}, native)
	if err != nil {
		t.Fatal(err)
	}
	if routes[0].Reasoning == nil {
		t.Fatal("JSONC source skipped")
	}
	exported, _ := base64.StdEncoding.DecodeString(native.Files["opencode/config/opencode/opencode.json"])
	if strings.Contains(string(exported), "private") {
		t.Fatal("unportable agent settings exported")
	}
	routes[0].Reasoning.Settings["agent"].(map[string]any)["build"].(map[string]any)["variant"] = "high"
	results, err := ReconcileLocalRoutes(context.Background(), home, routes)
	if err != nil || results[0].Status != "configured" {
		t.Fatalf("%v %v", results, err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".config/opencode/opencode.jsonc"))
	if !strings.Contains(string(raw), "Keep provider comments") || !strings.Contains(string(raw), "/Users/local/private") || !strings.Contains(string(raw), "high") {
		t.Fatalf("%s", raw)
	}
}
