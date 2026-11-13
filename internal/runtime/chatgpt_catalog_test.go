package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const gatewayCatalog = `{"models":[{"slug":"zai/glm-5.3","display_name":"GLM-5.3 · Z.ai","description":"GLM-5.3 · Z.ai via magpie","base_instructions":"thinking & responses <ok>","supported_reasoning_levels":[{"effort":"low","description":""},{"effort":"high","description":""},{"effort":"max","description":""}],"priority":61}]}
`

// Magpie writes magpie-models.json with Go's escaped JSON and a one-space
// indent, keeping the gateway's field order; Fennec's copy matches it byte for
// byte so the catalog compares equal to Magpie's own.
func TestFormatMagpieCatalogMatchesMagpieLayout(t *testing.T) {
	got, err := formatMagpieCatalog([]byte(gatewayCatalog))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n \"models\": [\n  {\n   \"slug\": \"zai/glm-5.3\",\n   \"display_name\": \"GLM-5.3 · Z.ai\",\n   \"description\": \"GLM-5.3 · Z.ai via magpie\",\n   \"base_instructions\": \"thinking \\u0026 responses \\u003cok\\u003e\",\n   \"supported_reasoning_levels\": [\n    {\n     \"effort\": \"low\",\n     \"description\": \"\"\n    },\n    {\n     \"effort\": \"high\",\n     \"description\": \"\"\n    },\n    {\n     \"effort\": \"max\",\n     \"description\": \"\"\n    }\n   ],\n   \"priority\": 61\n  }\n ]\n}"
	if string(got) != want {
		t.Fatalf("layout differs:\n%s", got)
	}
	for _, invalid := range []string{``, `{}`, `{"models":[]}`, `{"models":[{"display_name":"x"}]}`, `not json`} {
		if _, err := formatMagpieCatalog([]byte(invalid)); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func fakeCatalogGateway(t *testing.T, body *atomic.Value, status *atomic.Int32) *atomic.Int32 {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/codex/models" || r.URL.Query().Get("client_version") != "0.167.2" || r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if code := status.Load(); code != 0 {
			http.Error(w, "unavailable", int(code))
			return
		}
		w.Write([]byte(body.Load().(string)))
	}))
	t.Cleanup(server.Close)
	previousURL, previousVersion := magpieCatalogURL, codexClientVersion
	magpieCatalogURL = server.URL + "/v1/codex/models"
	codexClientVersion = func() string { return "0.167.2" }
	t.Cleanup(func() { magpieCatalogURL, codexClientVersion = previousURL, previousVersion })
	return &requests
}

// The catalog follows the gateway's Codex model list, is rewritten only when
// it changes, and survives a gateway that is briefly unavailable.
func TestSyncMagpieCatalogFollowsGateway(t *testing.T) {
	var body atomic.Value
	var status atomic.Int32
	body.Store(gatewayCatalog)
	fakeCatalogGateway(t, &body, &status)
	path := filepath.Join(t.TempDir(), magpieCatalogName)
	ctx := context.Background()
	if !syncMagpieCatalog(ctx, path) {
		t.Fatal("catalog not written")
	}
	first, _ := os.ReadFile(path)
	if !strings.Contains(string(first), `"display_name": "GLM-5.3 · Z.ai"`) {
		t.Fatalf("catalog content: %s", first)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	if !syncMagpieCatalog(ctx, path) {
		t.Fatal("unchanged catalog reported missing")
	}
	if info, _ := os.Stat(path); !info.ModTime().Equal(old) {
		t.Fatal("unchanged catalog was rewritten")
	}
	body.Store(strings.Replace(gatewayCatalog, `"priority":61`, `"priority":7`, 1))
	if !syncMagpieCatalog(ctx, path) {
		t.Fatal("changed catalog not written")
	}
	if updated, _ := os.ReadFile(path); !strings.Contains(string(updated), `"priority": 7`) {
		t.Fatalf("catalog not refreshed: %s", updated)
	}
	status.Store(http.StatusBadGateway)
	if !syncMagpieCatalog(ctx, path) {
		t.Fatal("last catalog dropped while the gateway is unavailable")
	}
	status.Store(0)
	body.Store(`{"models":[]}`)
	if !syncMagpieCatalog(ctx, path) {
		t.Fatal("an empty gateway answer replaced the catalog")
	}
	if kept, _ := os.ReadFile(path); !strings.Contains(string(kept), `"priority": 7`) {
		t.Fatal("invalid catalog overwrote the last one")
	}
}

func TestSyncMagpieCatalogWithoutCodexOrGateway(t *testing.T) {
	var body atomic.Value
	var status atomic.Int32
	body.Store(gatewayCatalog)
	requests := fakeCatalogGateway(t, &body, &status)
	codexClientVersion = func() string { return "" }
	path := filepath.Join(t.TempDir(), magpieCatalogName)
	if syncMagpieCatalog(context.Background(), path) || requests.Load() != 0 {
		t.Fatal("catalog requested without a known Codex version")
	}
	codexClientVersion = func() string { return "0.167.2" }
	status.Store(http.StatusUnauthorized)
	if syncMagpieCatalog(context.Background(), path) {
		t.Fatal("catalog reported without a gateway answer")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("catalog file written from an error")
	}
}

// The Codex version comes from the npm package behind the codex on PATH.
func TestInstalledCodexVersionReadsNpmPackage(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "agent-versions/codex-latest.x/lib/node_modules/@openai/codex")
	os.MkdirAll(filepath.Join(pkg, "bin"), 0700)
	os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@openai/codex","version":"0.167.2"}`), 0600)
	os.WriteFile(filepath.Join(pkg, "bin/codex.js"), []byte("#!/bin/sh\n"), 0700)
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0700)
	os.Symlink(filepath.Join(pkg, "bin/codex.js"), filepath.Join(bin, "codex"))
	t.Setenv("PATH", bin)
	if got := installedCodexVersion(); got != "0.167.2" {
		t.Fatalf("version %q", got)
	}
	os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@openai/codex","version":"1.0 && x"}`), 0600)
	if got := installedCodexVersion(); got != "" {
		t.Fatalf("accepted version %q", got)
	}
	t.Setenv("PATH", "")
	if got := installedCodexVersion(); got != "" {
		t.Fatalf("version without codex %q", got)
	}
}

// ChatGPT's config names Fennec's catalog only when it runs with the magpie
// provider and the gateway names none; a catalog the gateway names wins, and
// a missing catalog leaves the key out.
func TestChatGPTConfigNamesMagpieCatalog(t *testing.T) {
	directory := t.TempDir()
	gateway := filepath.Join(directory, "gateway.toml")
	chatgpt := filepath.Join(directory, "chatgpt.toml")
	catalog := filepath.Join(directory, magpieCatalogName)
	read := func() string {
		data, _ := os.ReadFile(chatgpt)
		return string(data)
	}
	os.WriteFile(gateway, []byte("model = 'zai/glm-5.3'\nopenai_base_url = 'http://127.0.0.1:3425/backend-api/codex'\n"), 0600)
	if err := syncChatGPTConfig(chatgpt, gateway, func() string { t.Fatal("catalog asked without the magpie provider"); return "" }); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(), "model_catalog_json") {
		t.Fatal("catalog set without the magpie provider")
	}
	os.WriteFile(gateway, []byte("model = 'zai/glm-5.3'\n\n[model_providers.magpie]\nbase_url = 'http://127.0.0.1:3425/v1'\n"), 0600)
	if err := syncChatGPTConfig(chatgpt, gateway, func() string { return catalog }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(), "model_catalog_json = '"+catalog+"'") {
		t.Fatal("catalog not named:", read())
	}
	if err := syncChatGPTConfig(chatgpt, gateway, func() string { return "" }); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(), "model_catalog_json") {
		t.Fatal("unavailable catalog still named")
	}
	os.WriteFile(gateway, []byte("model = 'zai/glm-5.3'\nmodel_catalog_json = '/gateway/magpie-models.json'\n\n[model_providers.magpie]\nbase_url = 'http://127.0.0.1:3425/v1'\n"), 0600)
	if err := syncChatGPTConfig(chatgpt, gateway, func() string { return catalog }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(), "model_catalog_json = '/gateway/magpie-models.json'") {
		t.Fatal("gateway catalog not kept:", read())
	}
}

// End to end through the gateway home: the ChatGPT home gets Magpie's catalog
// and names it, and the app-server signature changes with the catalog so a
// running server restarts to load it.
func TestGatewayHomeCarriesMagpieCatalog(t *testing.T) {
	var body atomic.Value
	var status atomic.Int32
	body.Store(gatewayCatalog)
	fakeCatalogGateway(t, &body, &status)
	home := appServerHome(t, false)
	gateway := filepath.Join(home, ".codex")
	os.MkdirAll(gateway, 0700)
	os.WriteFile(filepath.Join(gateway, "config.toml"), []byte("model = 'zai/glm-5.3'\n\n[model_providers.magpie]\nbase_url = 'http://127.0.0.1:3425/v1'\n"), 0600)
	_, _, codexHome, err := codexAppServerEnvironment(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(codexHome, magpieCatalogName)
	config, _ := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if !strings.Contains(string(config), "model_catalog_json = '"+catalog+"'") {
		t.Fatal("ChatGPT config lacks the catalog:", string(config))
	}
	if data, err := os.ReadFile(catalog); err != nil || !strings.Contains(string(data), "GLM-5.3 · Z.ai") {
		t.Fatal("catalog missing", err)
	}
	first := catalogSignature(codexHome)
	if !strings.HasPrefix(first, "model_catalog_json sha256:") {
		t.Fatal("signature", first)
	}
	body.Store(strings.Replace(gatewayCatalog, "GLM-5.3 · Z.ai", "GLM-5.4 · Z.ai", 2))
	if _, _, _, err := codexAppServerEnvironment(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if second := catalogSignature(codexHome); second == first || second == "" {
		t.Fatal("signature did not follow the catalog")
	}
}
