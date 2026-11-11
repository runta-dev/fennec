package setup

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeModelPolicyRepairsGatewayLeak(t *testing.T) {
	ctx := context.Background()
	home, remote := t.TempDir(), t.TempDir()
	nativeFixture(t, home, ".codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"access_token":"fixture"}}`)
	local := "model = 'kimi-code/k3.1-pro-highspeed'\nmodel_reasoning_effort = 'high'\n"
	nativeFixture(t, home, ".codex/config.toml", local)
	source := NativeSource{Home: home}
	req, err := source.Gather(ctx, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	exported, _ := base64.StdEncoding.DecodeString(req.Files["codex/config.toml"])
	if strings.Contains(string(exported), "kimi") || strings.Contains(string(exported), "reasoning_effort") {
		t.Fatal("gateway choice leaked into native export")
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".codex/config.toml"))
	if string(raw) != local {
		t.Fatal("local configuration changed")
	}
	// An older client may still export a poisoned native config: enforce remotely.
	req.Files["codex/config.toml"] = base64.StdEncoding.EncodeToString([]byte(local))
	if _, err = ApplyNativeAccounts(ctx, remote, req); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(remote, ".local/fennec/native/codex/config.toml")
	raw, _ = os.ReadFile(target)
	if strings.Contains(string(raw), "kimi") {
		t.Fatal("remote boundary allowed foreign model")
	}
	nativeFixture(t, remote, ".local/fennec/native/codex/config.toml", "model='gpt-fixture'\n")
	if _, err = ApplyNativeAccounts(ctx, remote, req); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(target)
	if !strings.Contains(string(raw), "gpt-fixture") {
		t.Fatal("valid cloud choice lost")
	}
}
func TestNativeModelsCatalogAndSet(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	nativeFixture(t, home, ".local/fennec/native/codex/config.toml", "cli_auth_credentials_store='file'\n")
	nativeFixture(t, home, ".local/fennec/native/codex/models_cache.json", `{"models":[{"slug":"gpt-fixture","display_name":"GPT Fixture","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},{"slug":"gpt-hidden","visibility":"hide"},{"slug":"kimi-code/k3","display_name":"Foreign"}]}`)
	state, err := NativeModels(ctx, home, "codex", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Models) != 2 {
		t.Fatalf("wrong choices: %+v", state.Models)
	}
	for _, choice := range []NativeModelSelection{{Agent: "codex", Model: "kimi-code/k3"}, {Agent: "codex", Model: "gpt-hidden"}, {Agent: "codex", Model: "gpt-unknown"}, {Agent: "codex", Model: "gpt-fixture", Effort: "max"}} {
		if _, err = SetNativeModel(ctx, home, choice); err == nil {
			t.Fatalf("accepted %+v", choice)
		}
	}
	state, err = SetNativeModel(ctx, home, NativeModelSelection{Agent: "codex", Model: "gpt-fixture", Effort: "high"})
	if err != nil || state.Model != "gpt-fixture" || state.Effort != "high" {
		t.Fatal(state, err)
	}
	if err = ConfigureAgentMode(home, false); err != nil {
		t.Fatal(err)
	}
	if _, err = SetNativeModel(ctx, home, NativeModelSelection{Agent: "codex"}); err == nil {
		t.Fatal("native write allowed in gateway mode")
	}
}
func TestNativeModelsPiConnectedProviders(t *testing.T) {
	home := t.TempDir()
	nativeFixture(t, home, ".local/fennec/native/pi/settings.json", `{"defaultProvider":"magpie","defaultModel":"codex/gpt-test"}`)
	nativeFixture(t, home, ".local/fennec/native/pi/auth.json", `{"anthropic":{"key":"fixture"},"magpie":{"key":"fixture"}}`)
	nativeFixture(t, home, ".local/fennec/native/pi/models.json", `{"providers":{"anthropic":{"models":[{"id":"claude-test"}]},"openai":{"models":[{"id":"gpt-test"}]},"magpie":{"models":[{"id":"any"}]}}}`)
	state, err := NativeModels(context.Background(), home, "pi", false)
	if err != nil || len(state.Models) != 2 || state.Models[1].ID != "anthropic/claude-test" || state.Model != "" {
		t.Fatal(state, err)
	}
}
func TestNativeModelMalformedBaselineDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	root := ".local/fennec/native/"
	original := "model='gpt-before'\n"
	nativeFixture(t, home, root+"codex/config.toml", original)
	nativeFixture(t, home, root+".baseline.json", "invalid")
	if err := writeNativeSelection(home, NativeModelSelection{Agent: "codex"}); err == nil {
		t.Fatal("malformed baseline accepted")
	}
	raw, _ := os.ReadFile(filepath.Join(home, root, "codex/config.toml"))
	if string(raw) != original {
		t.Fatal("config mutated before baseline validation")
	}
}

func TestNativeClaudeCatalogExcludesDeniedAndForeignModels(t *testing.T) {
	home := t.TempDir()
	nativeFixture(t, home, ".local/fennec/native/claude/.claude.json", `{"additionalModelOptionsCache":[{"value":"claude-denied","label":"Denied"},{"value":"claude-allowed","label":"Allowed"},{"value":"codex/gpt-test","label":"Foreign"}],"modelAccessCache":[{"apiName":"claude-denied","entitled":false},{"apiName":"claude-allowed","entitled":true}]}`)
	state, err := NativeModels(context.Background(), home, "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range state.Models {
		if m.ID == "claude-denied" || strings.Contains(m.ID, "gpt") {
			t.Fatalf("invalid model offered: %s", m.ID)
		}
	}
	raw, err := sanitizeNativeModelFile("claude/settings.json", []byte(`{"model":"codex/gpt-test","env":{"ANTHROPIC_MODEL":"kimi-code/k3"}}`))
	if err != nil || strings.Contains(string(raw), "gpt") || strings.Contains(string(raw), "kimi") {
		t.Fatal(string(raw), err)
	}
}

func TestNativeModelChoiceSurvivesResyncAndGatewayToggle(t *testing.T) {
	ctx := context.Background()
	home, remote := t.TempDir(), t.TempDir()
	nativeFixture(t, home, ".codex/auth.json", `{"tokens":{"access_token":"fixture"}}`)
	nativeFixture(t, home, ".codex/config.toml", "model='kimi-code/k3'\n")
	source := NativeSource{Home: home}
	req, err := source.Gather(ctx, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyNativeAccounts(ctx, remote, req); err != nil {
		t.Fatal(err)
	}
	nativeFixture(t, remote, ".local/fennec/native/codex/models_cache.json", `{"models":[{"slug":"gpt-fixture"}]}`)
	if _, err = SetNativeModel(ctx, remote, NativeModelSelection{Agent: "codex", Model: "gpt-fixture"}); err != nil {
		t.Fatal(err)
	}
	for _, gateway := range []bool{true, false} {
		source.Gateway = gateway
		req, err = source.Gather(ctx, []string{"codex"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = ApplyNativeAccounts(ctx, remote, req); err != nil {
			t.Fatal(err)
		}
		state, err := NativeModels(ctx, remote, "codex", false)
		if err != nil || state.Model != "gpt-fixture" {
			t.Fatal(state, err)
		}
	}
	if _, err = SetNativeModel(ctx, remote, NativeModelSelection{Agent: "codex"}); err != nil {
		t.Fatal(err)
	}
	if _, err = SetNativeModel(ctx, remote, NativeModelSelection{Agent: "codex", Model: "gpt-fixture"}); err != nil {
		t.Fatal(err)
	}
}
func TestNativePiRemovesDisconnectedProviderSelection(t *testing.T) {
	for _, provider := range []string{"magpie", "unconfigured"} {
		data, err := sanitizeNativePiSettings([]byte(`{"defaultProvider":"`+provider+`","defaultModel":"model","defaultThinkingLevel":"high"}`), map[string]bool{"anthropic": true})
		if err != nil || strings.Contains(string(data), "default") {
			t.Fatal(string(data), err)
		}
	}
}

func TestNativeModelPolicyUpgradePreservesCloudTUISettings(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	original := []byte("cli_auth_credentials_store='file'\nmodel='kimi-code/k3'\n")
	baseline := `{"codex/config.toml":{"source":"` + credentialHash(original) + `","remote":"` + credentialHash(original) + `"}}`
	nativeFixture(t, remote, ".local/fennec/native/.baseline.json", baseline)
	nativeFixture(t, remote, ".local/fennec/native/codex/config.toml", "cli_auth_credentials_store='file'\nmodel='gpt-fixture'\n[tui]\nnotifications=true\n")
	request := ModelAccountsRequest{Version: 1, Native: true, Files: map[string]string{"codex/config.toml": base64.StdEncoding.EncodeToString([]byte("cli_auth_credentials_store='file'\n"))}}
	if _, err := ApplyNativeAccounts(ctx, remote, request); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(remote, ".local/fennec/native/codex/config.toml"))
	if !strings.Contains(string(raw), "gpt-fixture") || !strings.Contains(string(raw), "notifications = true") {
		t.Fatal("cloud model or TUI settings lost", string(raw))
	}
	if compatibleNativeNonModel("codex/config.toml", []byte("model_provider='private'"), []byte("model_provider='other'")) {
		t.Fatal("overlapping provider conflict ignored")
	}
}

func TestNativePolicyDoesNotAcceptAnExtraCloudProvider(t *testing.T) {
	if compatibleNativeNonModel("codex/config.toml", []byte("model_provider='private'"), []byte("model='gpt-test'")) {
		t.Fatal("provider routing change was mistaken for TUI state")
	}
}
