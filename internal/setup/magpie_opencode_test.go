package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeMagpieConfigVersionsAndComments(t *testing.T) {
	for _, provider := range []string{`"provider":{"magpie":{"options":{"baseURL":"http://127.0.0.1:3425/v1","apiKey":"fixture"},"models":{"vendor/old":{},"vendor/new":{}}}}`, `"providers":{"magpie":{"settings":{"baseURL":"http://127.0.0.1:3425/v1","apiKey":"fixture"},"models":{"vendor/old":{},"vendor/new":{}}}}`} {
		home := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("OPENCODE_CONFIG_DIR", "")
		fixture := "{\n// Keep this comment and provider order.\n\"model\":\"magpie/vendor/old\",\n" + provider + ",\n}"
		nativeFixture(t, home, ".config/opencode/opencode.jsonc", fixture)
		nativeFixture(t, home, ".config/opencode/opencode.json", `{"model":"ignored"}`)
		if got, ok := readConfiguredMagpieModel(home, "opencode"); !ok || got != "vendor/old" {
			t.Fatal(got, ok)
		}
		if handled, err := writeConfiguredOpenCodeModel(home, "vendor/new"); !handled || err != nil {
			t.Fatal(handled, err)
		}
		got, _ := os.ReadFile(filepath.Join(home, ".config/opencode/opencode.jsonc"))
		if !strings.Contains(string(got), "// Keep this comment") || !strings.Contains(string(got), provider) {
			t.Fatal("provider or comments changed")
		}
		if got, ok := readConfiguredMagpieModel(home, "opencode"); !ok || got != "vendor/new" {
			t.Fatal(got, ok)
		}
		if handled, err := writeConfiguredOpenCodeModel(home, "vendor/missing"); handled || err != nil {
			t.Fatal(handled, err)
		}
		nativeFixture(t, home, ".config/opencode/opencode.jsonc", `{"model":"magpie/vendor/old","provider":{"magpie":{"options":{"baseURL":"http://localhost:3425/v1","apiKey":"fixture"}}},"providers":{"magpie":{"settings":{"baseURL":"https://other.example/v1","apiKey":"native"}}}}`)
		if _, ok := readConfiguredMagpieModel(home, "opencode"); ok {
			t.Fatal("shadowing v2 provider was ignored")
		}
	}
}
