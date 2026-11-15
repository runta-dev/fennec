package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeGatewayAliasReadbackPreservesContext(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, tc := range []struct{ name, model, pinned, override, want string }{
		{"alias context", "sonnet[1m]", "claude-opus-5-5[1m]", "", "claude/claude-opus-5-5[1m]"},
		{"alias adds context", "sonnet[1m]", "claude-opus-5-5", "", "claude/claude-opus-5-5[1m]"},
		{"pin context", "sonnet", "claude-opus-5-5[1m]", "", "claude/claude-opus-5-5[1m]"},
		{"qualified pin", "sonnet", "codex/gpt-6-astra", "", "codex/gpt-6-astra"},
		{"direct model", "claude-opus-5-5[1m]", "", "", "claude/claude-opus-5-5[1m]"},
		{"environment precedence", "sonnet", "claude-opus-5-5", "codex/gpt-6-astra", "codex/gpt-6-astra"},
		{"unresolved alias", "sonnet", "", "", ""},
		{"mixed planning alias", "opusplan", "claude-opus-5-5", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			data, _ := json.Marshal(map[string]any{"model": tc.model, "env": map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:3425", "ANTHROPIC_DEFAULT_SONNET_MODEL": tc.pinned, "ANTHROPIC_MODEL": tc.override}})
			nativeFixture(t, home, ".claude/settings.json", string(data))
			got, ok := readConfiguredMagpieModel(home, "claude")
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("got %q, %v; want %q", got, ok, tc.want)
			}
		})
	}
}

func TestClaudeContextRouteReconcilesWithoutWritingOrGateway(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	settings := `{"model":"sonnet[1m]","env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:3425","ANTHROPIC_DEFAULT_SONNET_MODEL":"claude-opus-5-5[1m]"}}`
	nativeFixture(t, home, ".claude/settings.json", settings)
	// An unchanged route must not spawn Magpie or query a gateway.
	t.Setenv("PATH", t.TempDir())
	results, err := ReconcileLocalRoutes(context.Background(), home, []AgentRoute{{Agent: "claude", Model: "claude/claude-opus-5-5"}})
	if err != nil || len(results) != 1 || results[0].Status != "configured" {
		t.Fatalf("reconcile = %+v, %v", results, err)
	}
	after, err := os.ReadFile(filepath.Join(home, ".claude/settings.json"))
	if err != nil || string(after) != settings {
		t.Fatal("readback changed the user's context preference")
	}
}

func TestConfiguredModelMatchesDoesNotHideWrongRoutes(t *testing.T) {
	for _, tc := range []struct {
		agent, actual, desired string
		want                   bool
	}{
		{"claude", "claude/claude-opus-5-5[1m]", "claude/claude-opus-5-5", true},
		{"claude", "claude/claude-sonnet-5-5[1m]", "claude/claude-opus-5-5", false},
		{"claude", "claude/claude-opus-5-5", "claude/claude-opus-5-5[1m]", false},
		{"claude", "other/claude-opus-5-5[1m]", "claude/claude-opus-5-5", false},
		{"claude", "codex/gpt-6-astra[1m]", "codex/gpt-6-astra", false},
		{"codex", "claude/claude-opus-5-5[1m]", "claude/claude-opus-5-5", false},
	} {
		if got := configuredModelMatches(tc.agent, tc.actual, tc.desired); got != tc.want {
			t.Errorf("%s: %q matches %q = %v", tc.agent, tc.actual, tc.desired, got)
		}
	}
}

func TestReadConfiguredMagpieModelMatchesGatewayConfigsAndRejectsNative(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PI_CODING_AGENT_DIR", "")

	nativeFixture(t, home, ".codex/config.toml", "model_provider = 'magpie'\nmodel = 'codex/gpt-6-astra'\n")
	if got, ok := readConfiguredMagpieModel(home, "codex"); !ok || got != "codex/gpt-6-astra" {
		t.Fatalf("expected codex magpie provider route, got %q %v", got, ok)
	}

	nativeFixture(t, home, ".codex/config.toml", "openai_base_url = 'http://127.0.0.1:3425/backend-api/codex'\nmodel = 'devin/gpt-6-astra-priority'\n")
	if got, ok := readConfiguredMagpieModel(home, "codex"); !ok || got != "devin/gpt-6-astra-priority" {
		t.Fatalf("expected codex base_url route, got %q %v", got, ok)
	}

	nativeFixture(t, home, ".codex/config.toml", "model = 'gpt-5'\n")
	if _, ok := readConfiguredMagpieModel(home, "codex"); ok {
		t.Fatal("accepted native codex config as magpie route")
	}

	nativeFixture(t, home, ".claude/settings.json", `{"model":"codex/gpt-6.1-sol","env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:3425"}}`)
	if got, ok := readConfiguredMagpieModel(home, "claude"); !ok || got != "codex/gpt-6.1-sol" {
		t.Fatalf("expected claude magpie route, got %q %v", got, ok)
	}

	nativeFixture(t, home, ".claude/settings.json", `{"model":"claude-sonnet-4-5"}`)
	if _, ok := readConfiguredMagpieModel(home, "claude"); ok {
		t.Fatal("accepted native claude config as magpie route")
	}

	nativeFixture(t, home, ".pi/agent/settings.json", `{"defaultProvider":"magpie","defaultModel":"magpie/codex/gpt-6.1-sol"}`)
	if got, ok := readConfiguredMagpieModel(home, "pi"); !ok || got != "codex/gpt-6.1-sol" {
		t.Fatalf("expected pi magpie route, got %q %v", got, ok)
	}

	out, ok := readConfiguredMagpieModelOutput(home, "pi")
	if !ok || string(out) != "Pi model codex/gpt-6.1-sol\n" {
		t.Fatalf("unexpected formatted output %q (filepath=%s)", out, filepath.Clean(home))
	}
}
