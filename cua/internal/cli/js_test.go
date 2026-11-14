package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/mcpclient"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/standalone"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParseJSArgs(t *testing.T) {
	o, err := ParseJSArgs([]string{`await cua.listApps()`}, env(nil))
	if err != nil || o.Code != `await cua.listApps()` || o.Stdin || o.Session != DefaultSession || o.Timeout != 5*time.Minute {
		t.Fatalf("code argument: %+v %v", o, err)
	}
	o, err = ParseJSArgs(nil, env(map[string]string{SessionEnv: "task-7"}))
	if err != nil || !o.Stdin || o.Session != "task-7" {
		t.Fatalf("stdin with the session from the environment: %+v %v", o, err)
	}
	o, err = ParseJSArgs([]string{"--session", "a.b_c-1", "--title", "Checking", "--out", "/tmp/x", "--timeout", "30s", "-"}, env(map[string]string{SessionEnv: "ignored"}))
	if err != nil || !o.Stdin || o.Session != "a.b_c-1" || o.Title != "Checking" || o.Out != "/tmp/x" || o.Timeout != 30*time.Second {
		t.Fatalf("flags: %+v %v", o, err)
	}
	for _, bad := range [][]string{{"--session", "no spaces"}, {"one", "two"}, {"--timeout", "0s", "x"}, {"--nope"}} {
		if _, err := ParseJSArgs(bad, env(nil)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if s, err := ParseSessionArgs([]string{"--session", "x"}, env(nil)); err != nil || s != "x" {
		t.Errorf("session args: %q %v", s, err)
	}
	if s, err := ParseSessionArgs(nil, env(nil)); err != nil || s != DefaultSession {
		t.Errorf("default session: %q %v", s, err)
	}
	if _, err := ParseSessionArgs([]string{"extra"}, env(nil)); err == nil {
		t.Error("extra argument accepted")
	}
}

func TestReadCode(t *testing.T) {
	if c, err := ReadCode(strings.NewReader("var a = 1;\n")); err != nil || c != "var a = 1;\n" {
		t.Fatalf("%q %v", c, err)
	}
	if _, err := ReadCode(strings.NewReader(" \n")); err == nil {
		t.Error("empty code accepted")
	}
	if _, err := ReadCode(strings.NewReader(strings.Repeat("x", MaxStdinCode+1))); err == nil {
		t.Error("oversized code accepted")
	}
}

func TestStartCommandStandaloneApp(t *testing.T) {
	app := filepath.Join(t.TempDir(), standalone.AppName)
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), standalone.InfoPlist("1.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := standalone.Executable(app)
	cmd := startCommand(exe, env(map[string]string{"FENNEC_CUA_DIR": "/d", "FENNEC_CUA_HTTP_TOKEN": "secret"}))
	got := strings.Join(cmd.Args[1:], " ")
	if cmd.Path != "/usr/bin/open" || got != "-n -g --env FENNEC_CUA_DIR=/d -a "+app+" --args serve" {
		t.Fatalf("standalone start: %s %s", cmd.Path, got)
	}
	if strings.Contains(got, "secret") {
		t.Fatal("the HTTP token went onto the command line")
	}
	for _, k := range PassedEnv {
		if strings.Contains(k, "TOKEN") || strings.Contains(k, "KEY") {
			t.Errorf("%s is passed", k)
		}
	}
}

// fakeDaemon serves MCP on a unix socket with named sessions, echoing the
// agent id so a test sees which session answered.
type echoHandler struct{}

func (echoHandler) Tools(*mcpserver.Session) []mcpserver.Tool {
	return []mcpserver.Tool{{Name: "js", InputSchema: []byte(`{"type":"object"}`)}, {Name: "js_reset", InputSchema: []byte(`{"type":"object"}`)}}
}

func (echoHandler) Call(_ context.Context, s *mcpserver.Session, name string, _ json.RawMessage) mcpserver.Result {
	return mcpserver.Result{Text: name + "@" + s.AgentID}
}

func shortSocket(t *testing.T) string {
	dir, err := os.MkdirTemp("/tmp", "fcua")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func TestJSUsesTheNamedSessionAndStartsTheDaemon(t *testing.T) {
	sock := shortSocket(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := 0
	start := func() (<-chan error, error) {
		started++
		ln, err := net.Listen("unix", sock)
		if err != nil {
			return nil, err
		}
		go func() { <-ctx.Done(); _ = ln.Close() }()
		info := mcpserver.Info{Name: "t", IDPrefix: "mcp", Named: &mcpserver.Named{}}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() { _ = mcpserver.Serve(ctx, c, c, echoHandler{}, info); _ = c.Close() }()
			}
		}()
		return nil, nil
	}
	if err := EnsureDaemon(ctx, sock, start, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDaemon(ctx, sock, start, 5*time.Second); err != nil || started != 1 {
		t.Fatalf("a running daemon was started again (%d starts, %v)", started, err)
	}
	res, err := JS(ctx, sock, JSOptions{Session: "work", Code: "1", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if failed, err := PrintResult(&sb, res, ""); err != nil || failed || strings.TrimSpace(sb.String()) != "js@mcp-session-work" {
		t.Fatalf("js answered %q (%v, %v)", sb.String(), failed, err)
	}
	if err := JSEnd(ctx, sock, "work"); err != nil {
		t.Fatal(err)
	}
	if mcpclient.SessionMetaKey != mcpserver.SessionMetaKey || mcpclient.EndSessionMethod != mcpserver.EndSessionMethod {
		t.Fatal("the client and the server name sessions differently")
	}
}

func TestEnsureDaemonReportsAnEarlyExit(t *testing.T) {
	sock := shortSocket(t)
	exited := make(chan error, 1)
	exited <- errors.New("exit status 2")
	err := EnsureDaemon(context.Background(), sock, func() (<-chan error, error) { return exited, nil }, time.Second)
	if err == nil || !strings.Contains(err.Error(), "exit status 2") {
		t.Fatalf("got %v", err)
	}
}
