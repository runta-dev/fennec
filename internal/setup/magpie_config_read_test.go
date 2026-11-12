package setup

import (
	"path/filepath"
	"testing"
)

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
