package runtime

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDialCodexAppServerReusesRunningSocket(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fennec-appserver-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PATH", "")
	socket := CodexAppServerSocket(home)
	if socket != filepath.Join(home, ".codex/app-server-control/app-server-control.sock") {
		t.Fatal(socket)
	}
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
	conn.Close()
	<-accepted
}

func TestDialCodexAppServerRequiresInstalledCodex(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "fennec-appserver-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PATH", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := DialCodexAppServer(ctx, home); err == nil || err.Error() != "codex is not installed in the runtime" {
		t.Fatal(err)
	}
}
