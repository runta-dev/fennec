package runtime

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func appServerHome(t *testing.T, native bool) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "fennec-appserver-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PATH", "")
	os.MkdirAll(filepath.Join(home, ".local/fennec"), 0700)
	mode := `{"native":false}`
	if native {
		mode = `{"native":true}`
	}
	if err := os.WriteFile(filepath.Join(home, ".local/fennec/agent-mode.json"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestDialCodexAppServerReusesGatewaySocket(t *testing.T) {
	home := appServerHome(t, false)
	socket := CodexAppServerSocket(filepath.Join(home, ".local/fennec/chatgpt/codex"))
	os.MkdirAll(filepath.Dir(socket), 0700)
	// The running server was started with the current (empty) option set.
	os.WriteFile(filepath.Join(filepath.Dir(socket), "fennec-app-server.args"), nil, 0600)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		if conn, err := listener.Accept(); err == nil {
			conn.Close()
			close(accepted)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := DialCodexAppServer(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	labeled, ok := conn.(interface{ StreamLabel() string })
	if !ok || labeled.StreamLabel() != CodexAppServerGateway {
		t.Fatal("gateway stream label missing")
	}
	<-accepted
}

func TestDialCodexAppServerRequiresInstalledCodex(t *testing.T) {
	home := appServerHome(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := DialCodexAppServer(ctx, home); err == nil || err.Error() != "codex is not installed in the runtime" {
		t.Fatal(err)
	}
}

// Native mode never falls back to the Magpie-routed ~/.codex server.
func TestDialCodexAppServerNativeModeNeedsNativeEnvironment(t *testing.T) {
	home := appServerHome(t, true)
	socket := CodexAppServerSocket(filepath.Join(home, ".codex"))
	os.MkdirAll(filepath.Dir(socket), 0700)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if conn, err := DialCodexAppServer(ctx, home); err == nil {
		conn.Close()
		t.Fatal("native mode reached the gateway app-server")
	}
}

// Magpie routing comes from the gateway home; the ChatGPT identity comes from
// the user's synchronized native account, never the gateway's credentials.
func TestGatewayHomeLinksRoutingAndUserAccount(t *testing.T) {
	home := appServerHome(t, false)
	gateway := filepath.Join(home, ".codex")
	native := filepath.Join(home, ".local/fennec/native/codex")
	for _, dir := range []string{gateway, native} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(gateway, "config.toml"), []byte("model = 'zai/glm-5.3'\n"), 0600)
	os.WriteFile(filepath.Join(gateway, "auth.json"), []byte(`{"account":"gateway"}`), 0600)
	os.WriteFile(filepath.Join(native, "auth.json"), []byte(`{"account":"user"}`), 0600)
	mode, env, codexHome, err := codexAppServerEnvironment(home)
	if err != nil {
		t.Fatal(err)
	}
	if mode != CodexAppServerGateway || codexHome != filepath.Join(home, ".local/fennec/chatgpt/codex") {
		t.Fatal(mode, codexHome)
	}
	found := false
	for _, entry := range env {
		if entry == "CODEX_HOME="+codexHome {
			found = true
		}
	}
	if !found {
		t.Fatal("app-server environment does not use the ChatGPT home")
	}
	config, _ := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	auth, err := os.ReadFile(filepath.Join(codexHome, "auth.json"))
	if !strings.Contains(string(config), "zai/glm-5.3") || err != nil || string(auth) != `{"account":"user"}` {
		t.Fatalf("config=%q auth=%q err=%v", config, auth, err)
	}
	if info, err := os.Lstat(filepath.Join(codexHome, "config.toml")); err != nil || !info.Mode().IsRegular() {
		t.Fatal("ChatGPT config must be its own file", err)
	}
	if gatewayAuth, _ := os.ReadFile(filepath.Join(gateway, "auth.json")); string(gatewayAuth) != `{"account":"gateway"}` {
		t.Fatal("gateway credentials changed")
	}
}

func TestGatewayOverridesSelectMagpieWithoutOpenAILogin(t *testing.T) {
	home := t.TempDir()
	if overrides := gatewayOverrides(home); overrides != nil {
		t.Fatal("overrides without a Magpie provider", overrides)
	}
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = 'zai/glm-5.3'\n\n[model_providers.magpie]\nrequires_openai_auth = true\n"), 0600)
	got := strings.Join(gatewayOverrides(home), " ")
	if got != `-c model_provider="magpie" -c model_providers.magpie.requires_openai_auth=false` {
		t.Fatal(got)
	}
}

// ChatGPT writes stay in its own config; routing follows the gateway; Mac-only
// MCP servers are removed from both files without touching anything else.
func TestChatGPTConfigKeepsDesktopWritesOutOfTheGateway(t *testing.T) {
	directory := t.TempDir()
	gateway := filepath.Join(directory, "gateway.toml")
	chatgpt := filepath.Join(directory, "chatgpt.toml")
	os.WriteFile(gateway, []byte(`model = "zai/glm-5.3"
openai_base_url = "http://127.0.0.1:3425/backend-api/codex"

[mcp_servers.node_repl]
command = "/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node_repl"

[mcp_servers.team]
command = "/usr/local/bin/team-mcp"
`), 0600)
	os.WriteFile(chatgpt, []byte(`model = "old"
approval_policy = "never"

[mcp_servers.node_repl]
command = "/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node_repl"

[mcp_servers.local_tool]
command = "npx"
`), 0600)
	if err := syncChatGPTConfig(chatgpt, gateway); err != nil {
		t.Fatal(err)
	}
	gatewayData, _ := os.ReadFile(gateway)
	if strings.Contains(string(gatewayData), "node_repl") || !strings.Contains(string(gatewayData), "team-mcp") {
		t.Fatal("gateway cleanup wrong:", string(gatewayData))
	}
	config, _ := os.ReadFile(chatgpt)
	text := string(config)
	if !strings.Contains(text, "zai/glm-5.3") || strings.Contains(text, `"old"`) || !strings.Contains(text, "approval_policy") || strings.Contains(text, "node_repl") || !strings.Contains(text, "local_tool") || !strings.Contains(text, "3425") {
		t.Fatal("ChatGPT config wrong:", text)
	}
}
