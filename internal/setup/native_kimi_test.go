package setup

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

const kimiFixtureConfig = `default_model = "kimi-code/k3.1-pro"

[providers."managed:kimi-code"]
type = "kimi"
api_key = ""
base_url = "https://api.kimi.com/coding/v1"

[providers."managed:kimi-code".oauth]
storage = "file"
key = "oauth/kimi-code"

[providers.magpie]
type = "kimi"
api_key = "local-gateway"
base_url = "http://127.0.0.1:3425/v1"

[providers.lan]
type = "openai"
api_key = "lan-key"
base_url = "http://localhost:8080/v1"

[models."kimi-code/k3.1-pro"]
provider = "managed:kimi-code"
model = "k3.1-pro"
display_name = "K3.1 Pro"

[models."magpie/auto/claude-opus-5-5"]
provider = "magpie"
model = "auto/claude-opus-5-5"

[thinking]
enabled = true

[services.moonshot_search.oauth]
storage = "file"
key = "oauth/kimi-code"

[[hooks]]
event = "PreToolUse"
command = "/Users/me/bin/local-hook"
`

const kimiFixtureCredential = `{"access_token":"kimi-access","refresh_token":"kimi-refresh","expires_at":1794620000,"scope":"kimi-code","token_type":"Bearer","expires_in":900}`

func TestKimiCloudConfigKeepsOnlyPortableParts(t *testing.T) {
	out, err := kimiCloudConfig([]byte(kimiFixtureConfig))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["default_model"] != "kimi-code/k3.1-pro" || cfg["thinking"] == nil || cfg["services"] == nil {
		t.Fatalf("lost portable settings: %s", out)
	}
	providers, _ := cfg["providers"].(map[string]any)
	if providers["managed:kimi-code"] == nil || providers["magpie"] != nil || providers["lan"] != nil {
		t.Fatalf("providers: %v", providers)
	}
	models, _ := cfg["models"].(map[string]any)
	if models["kimi-code/k3.1-pro"] == nil || models["magpie/auto/claude-opus-5-5"] != nil {
		t.Fatalf("models: %v", models)
	}
	if strings.Contains(string(out), "hooks") || strings.Contains(string(out), "local-hook") || strings.Contains(string(out), "127.0.0.1") {
		t.Fatalf("local commands or loopback reached the cloud copy: %s", out)
	}

	// A default model routed through the local gateway is not carried over.
	out, _ = kimiCloudConfig([]byte(strings.Replace(kimiFixtureConfig, `default_model = "kimi-code/k3.1-pro"`, `default_model = "magpie/auto/claude-opus-5-5"`, 1)))
	cfg = nil
	_ = toml.Unmarshal(out, &cfg)
	if _, ok := cfg["default_model"]; ok {
		t.Fatalf("default model naming a dropped model kept: %s", out)
	}
}

func TestKimiGatherApplyAndLaunch(t *testing.T) {
	home, remote := t.TempDir(), t.TempDir()
	nativeFixture(t, home, ".kimi-code/config.toml", kimiFixtureConfig)
	nativeFixture(t, home, ".kimi-code/credentials/kimi-code.json", kimiFixtureCredential)
	nativeFixture(t, home, ".kimi-code/region", "cn\n")
	nativeFixture(t, home, ".kimi-code/sessions/s1/history.jsonl", `{"private":"session"}`)
	source := NativeSource{Home: home, Env: func(string) string { return "" }}
	request, err := source.Gather(context.Background(), []string{"kimi"})
	if err != nil {
		t.Fatal(err)
	}
	for name := range request.Files {
		if !strings.HasPrefix(name, "kimi/") && name != "env.json" {
			t.Fatalf("unexpected file %s", name)
		}
	}
	for _, name := range []string{kimiConfigFile, kimiCredentialFile, kimiRegionFile} {
		if request.Files[name] == "" {
			t.Fatalf("missing %s", name)
		}
	}
	if raw, _ := base64.StdEncoding.DecodeString(request.Files[kimiRegionFile]); string(raw) != "cn" {
		t.Fatalf("region %q", raw)
	}
	result, err := ApplyModelAccounts(context.Background(), remote, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NativeReady["kimi"] {
		t.Fatal("kimi not ready after sync")
	}
	root := filepath.Join(remote, ".local/fennec/native")
	if b, _ := os.ReadFile(filepath.Join(root, kimiRegionFile)); string(b) != "cn" {
		t.Fatalf("cloud region %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, kimiCredentialFile)); !strings.Contains(string(b), "kimi-refresh") {
		t.Fatal("credential not written")
	}
	if _, err := os.Stat(filepath.Join(root, "kimi/sessions")); !os.IsNotExist(err) {
		t.Fatal("sessions copied")
	}
	if !nativeHasCredentials(root, "kimi") {
		t.Fatal("credential not recognized")
	}

	env, err := nativeAgentEnvironment(remote, "kimi")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range env {
		if entry == kimiHomeEnv+"="+filepath.Join(root, "kimi") {
			found = true
		}
	}
	if !found {
		t.Fatalf("KIMI_CODE_HOME not set for the native agent: %v", env)
	}

	view := nativeAccountView("kimi", func(name string) []byte { b, _ := os.ReadFile(filepath.Join(root, name)); return b })
	if view.Status != "signed_in" || len(view.Accounts) != 1 || view.Accounts[0].Provider != "kimi" || view.Accounts[0].AuthType != "oauth" {
		t.Fatalf("account view: %+v", view)
	}
}

func TestKimiRegionRejectsAnythingButOneWord(t *testing.T) {
	for _, bad := range []string{"", "cn global", "../x", "CN", `{"a":1}`} {
		if _, ok := kimiRegion([]byte(bad)); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	home := t.TempDir()
	nativeFixture(t, home, ".kimi-code/region", "cn; rm -rf /")
	if _, err := (NativeSource{Home: home, Env: func(string) string { return "" }}).Gather(context.Background(), []string{"kimi"}); err == nil {
		t.Fatal("invalid region accepted")
	}
}

func TestKimiCredentialEntryUsesSeconds(t *testing.T) {
	e := kimiEntry(map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": float64(1794620000)})
	if e.expires != 1794620000000 || e.refresh != "r" || e.signedOut() {
		t.Fatalf("%+v", e)
	}
	// Same sign-in on both computers: the copy that expires later wins.
	cloud := []byte(`{"access_token":"old","refresh_token":"r","expires_at":1794620000}`)
	mac := []byte(`{"access_token":"new","refresh_token":"r","expires_at":1794629000}`)
	got, ok := resolveNativeCredential(context.Background(), kimiCredentialFile, cloud, mac)
	if !ok || !strings.Contains(string(got), `"new"`) {
		t.Fatalf("kept %s (%v)", got, ok)
	}
	// A signed-out copy never wins.
	got, ok = resolveNativeCredential(context.Background(), kimiCredentialFile, []byte(`{}`), mac)
	if !ok || !strings.Contains(string(got), `"new"`) {
		t.Fatalf("signed-out copy won: %s", got)
	}
}

func TestKimiSettingsMergeKeepsCloudGatewayEntries(t *testing.T) {
	cloud := []byte(`default_model = "magpie/zai/glm-5.3"
[providers.magpie]
base_url = "http://127.0.0.1:3425/v1"
[models."magpie/zai/glm-5.3"]
provider = "magpie"
[models."kimi-code/k3"]
provider = "managed:kimi-code"
display_name = "cloud name"
`)
	local := []byte(`default_model = "kimi-code/k3"
[providers."managed:kimi-code"]
base_url = "https://api.kimi.com/coding/v1"
[models."kimi-code/k3"]
provider = "managed:kimi-code"
display_name = "K3"
`)
	out, ok := mergeNativeSettings(kimiConfigFile, cloud, local)
	if !ok {
		t.Fatal("not merged")
	}
	var cfg map[string]any
	if err := toml.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	providers, _ := cfg["providers"].(map[string]any)
	models, _ := cfg["models"].(map[string]any)
	k3, _ := models["kimi-code/k3"].(map[string]any)
	if providers["magpie"] == nil || providers["managed:kimi-code"] == nil || models["magpie/zai/glm-5.3"] == nil || k3["display_name"] != "K3" {
		t.Fatalf("merge lost entries: %s", out)
	}
	if cfg["default_model"] != "magpie/zai/glm-5.3" {
		t.Fatalf("cloud model choice not kept: %v", cfg["default_model"])
	}
}

func TestKimiNativeModels(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".local/fennec/native")
	nativeFixture(t, root, kimiConfigFile, `default_model = "kimi-code/k3"
[models."kimi-code/k3"]
provider = "managed:kimi-code"
display_name = "K3"
[models."kimi-code/k3.1-pro"]
provider = "managed:kimi-code"
display_name = "K3.1 Pro"
`)
	state, err := NativeModels(context.Background(), home, "kimi", false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Model != "kimi-code/k3" || len(state.Models) != 3 || state.Models[2].Name != "K3.1 Pro" {
		t.Fatalf("%+v", state)
	}
	if err := writeNativeSelection(home, NativeModelSelection{Agent: "kimi", Model: "kimi-code/k3.1-pro"}); err != nil {
		t.Fatal(err)
	}
	if state, _ = NativeModels(context.Background(), home, "kimi", false); state.Model != "kimi-code/k3.1-pro" {
		t.Fatalf("selection not written: %+v", state)
	}
	if err := writeNativeSelection(home, NativeModelSelection{Agent: "kimi", Model: "kimi-code/unknown"}); err == nil {
		t.Fatal("unknown model accepted")
	}
	if NativeModelFamily("kimi", "magpie/zai/glm") || !NativeModelFamily("kimi", "kimi-code/k3") {
		t.Fatal("model family")
	}
}
