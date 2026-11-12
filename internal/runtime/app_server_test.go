package runtime

import (
	"context"
	"net"
	"os"
	"path/filepath"
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
	if string(config) != "model = 'zai/glm-5.3'\n" || err != nil || string(auth) != `{"account":"user"}` {
		t.Fatalf("config=%q auth=%q err=%v", config, auth, err)
	}
	// Codex may rewrite a linked file in place; the next dial restores the link.
	os.Remove(filepath.Join(codexHome, "config.toml"))
	os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("stale"), 0600)
	if _, _, _, err := codexAppServerEnvironment(home); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(codexHome, "config.toml")); err != nil || target != filepath.Join(gateway, "config.toml") {
		t.Fatal(target, err)
	}
	if gatewayAuth, _ := os.ReadFile(filepath.Join(gateway, "auth.json")); string(gatewayAuth) != `{"account":"gateway"}` {
		t.Fatal("gateway credentials changed")
	}
}
