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
	socket := CodexAppServerSocket(filepath.Join(home, ".codex"))
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
