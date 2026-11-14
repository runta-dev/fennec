package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestGrokMagpieModelSwitchPreservesProviderAndDisablesCampaigns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", "")
	const fixture = `[features]
campaigns = true
[models]
default = 'magpie/provider/old'
default_reasoning_effort = 'ultra'
[model.'magpie/provider/old']
model = 'provider/old'
base_url = 'http://127.0.0.1:3425/v1'
api_key = 'gateway-fixture'
[model.'magpie/provider/new']
model = 'provider/new'
base_url = 'http://127.0.0.1:3425/v1'
api_key = 'gateway-fixture'
api_backend = 'responses'
context_window = 123456
reasoning_efforts = ['low', 'high']
[model.native]
model = 'native-model'
base_url = 'https://api.x.ai/v1'
`
	nativeFixture(t, home, ".grok/config.toml", fixture)
	if got, ok := readConfiguredMagpieModel(home, "grok"); !ok || got != "provider/old" {
		t.Fatal(got, ok)
	}
	if handled, err := writeConfiguredGrokModel(home, "provider/new"); !handled || err != nil {
		t.Fatal(handled, err)
	}
	if got, ok := readConfiguredMagpieModel(home, "grok"); !ok || got != "provider/new" {
		t.Fatal(got, ok)
	}
	path := filepath.Join(home, ".grok/config.toml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(before, &cfg); err != nil {
		t.Fatal(err)
	}
	models := cfg["models"].(map[string]any)
	if cfg["features"].(map[string]any)["campaigns"] != false || models["default_reasoning_effort"] != nil {
		t.Fatal("campaign or incompatible effort can override the selected model")
	}
	tables := cfg["model"].(map[string]any)
	selected := tables["magpie/provider/new"].(map[string]any)
	if selected["api_backend"] != "responses" || selected["context_window"] != int64(123456) || selected["api_key"] != "gateway-fixture" || tables["native"] == nil {
		t.Fatal("provider settings changed during model selection")
	}
	if handled, err := writeConfiguredGrokModel(home, "provider/missing"); handled || err != nil {
		t.Fatal("unknown model did not fall back to Magpie's writer", handled, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("missing model mutated the configuration")
	}
	nativeFixture(t, home, ".grok/config.toml", "[models]\ndefault = 'magpie/provider/new'\n[model.'magpie/provider/new']\nmodel='provider/new'\nbase_url='https://api.x.ai/v1'\napi_key='native-key'\n")
	if _, ok := readConfiguredMagpieModel(home, "grok"); ok {
		t.Fatal("native endpoint mistaken for Magpie")
	}
}
