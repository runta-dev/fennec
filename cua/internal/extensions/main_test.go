package extensions_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"fennec/cua/internal/daemon"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/extensions"
	"fennec/cua/internal/protocol"
)

// The test binary doubles as every plugin and service the tests start: with
// EXT_CHILD=1 it runs child instead of the tests.
func TestMain(m *testing.M) {
	if os.Getenv("EXT_CHILD") == "1" {
		child()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func child() {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-test.") {
		service(os.Args[1], os.Args[2:])
		return
	}
	plugin()
}

// plugin runs one script plugin request; params.mode picks the behaviour.
func plugin() {
	in := bufio.NewReader(os.Stdin)
	line, err := in.ReadBytes('\n')
	if err != nil {
		os.Exit(9)
	}
	var req protocol.RunRequest
	if err := json.Unmarshal(line, &req); err != nil {
		os.Exit(9)
	}
	report := os.NewFile(3, "report")
	write := func(v any) {
		data, _ := json.Marshal(v)
		_, _ = report.Write(data)
		_ = report.Close()
	}
	mode, _ := req.Params["mode"].(string)
	switch mode {
	case "echo":
		fmt.Println("hello from the plugin")
		env := map[string]string{}
		for _, k := range []string{extensions.EnvDir, extensions.EnvEmbedded, extensions.EnvPlugin, extensions.EnvAgent, extensions.EnvInput, extensions.EnvReportFD, "OPENROUTER_API_KEY", "MY_KEY", "PLAIN_VAR"} {
			if v, ok := os.LookupEnv(k); ok {
				env[k] = v
			}
		}
		_, hasRun := os.LookupEnv(extensions.EnvRun)
		write(map[string]any{"result": map[string]any{"params": req.Params, "code": req.Code, "env": env, "run_token": hasRun}, "usage": map[string]any{"m": map[string]int{"calls": 1}}})
	case "error":
		write(map[string]any{"error": map[string]string{"kind": "exception", "message": "boom"}})
	case "crash":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 5000)+"\nTAILMARK the end\n")
		os.Exit(3)
	case "term":
		// Cleans up on SIGTERM, as a well-behaved plugin does.
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		fmt.Println("waiting")
		<-sig
		fmt.Println("cleaned up")
		os.Exit(0)
	case "ignore_term":
		signal.Ignore(syscall.SIGTERM)
		fmt.Println("stubborn")
		time.Sleep(time.Hour)
	case "desk":
		// The plugin's own desk call through the daemon's socket.
		out, err := mcpCall(os.Getenv(extensions.EnvDir), os.Getenv(extensions.EnvRun), "act", `{}`)
		if err != nil {
			write(map[string]any{"error": map[string]string{"kind": "exception", "message": err.Error()}})
			return
		}
		write(map[string]any{"result": json.RawMessage(out)})
	default:
		write(map[string]any{"result": "no mode"})
	}
}

// mcpCall makes one tools/call on the daemon at dir, presenting token.
func mcpCall(dir, token, tool, args string) (string, error) {
	conn, err := net.Dial("unix", daemon.PathsIn(dir).MCP)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	init := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"plugin"},"_meta":{%q:%q}}}`, extensions.RunMeta, token)
	if _, err := fmt.Fprintln(conn, init); err != nil {
		return "", err
	}
	if _, err := r.ReadBytes('\n'); err != nil {
		return "", err
	}
	if _, err := fmt.Fprintf(conn, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s}}`+"\n", tool, args); err != nil {
		return "", err
	}
	line, err := r.ReadBytes('\n')
	if err != nil {
		return "", err
	}
	var rep struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &rep); err != nil || len(rep.Result.Content) == 0 {
		return "", fmt.Errorf("bad reply %s", line)
	}
	if rep.Result.IsError {
		return "", fmt.Errorf("%s", rep.Result.Content[0].Text)
	}
	return rep.Result.Content[0].Text, nil
}

// service runs a test service: mode serve answers its tools, mode stale is
// a copy left by a killed daemon, mode sleep idles.
func service(mode string, args []string) {
	state := os.Getenv("EXT_STATE")
	switch mode {
	case "sleep":
		time.Sleep(time.Hour)
	case "stale":
		// args: socket, "ignore" to ignore SIGTERM.
		if len(args) > 1 && args[1] == "ignore" {
			signal.Ignore(syscall.SIGTERM)
		}
		ln, err := net.Listen("unix", args[0])
		if err != nil {
			os.Exit(7)
		}
		for {
			c, err := ln.Accept()
			if err != nil {
				os.Exit(7)
			}
			go func() { _, _ = io.Copy(io.Discard, c); _ = c.Close() }()
		}
	case "serve":
	default:
		os.Exit(8)
	}
	if len(args) > 0 && args[0] == "ignore" {
		signal.Ignore(syscall.SIGTERM)
	}
	// A grandchild in the service's process group, which must end with it.
	kid := exec.Command(os.Args[0], "sleep")
	if kid.Start() == nil {
		_ = os.WriteFile(filepath.Join(state, "kid-"+strconv.Itoa(os.Getpid())), []byte(strconv.Itoa(kid.Process.Pid)), 0o600)
	}
	sock := os.Getenv(extensions.EnvServiceSocket)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(7)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			os.Exit(7)
		}
		go answer(c.(*net.UnixConn), state)
	}
}

func answer(c *net.UnixConn, state string) {
	defer c.Close()
	r := bufio.NewReaderSize(c, 1<<16)
	// Peek the tool without reading the whole request.
	head, _ := r.Peek(64)
	tool := ""
	if i := strings.Index(string(head), `"tool":"`); i >= 0 {
		rest := string(head[i+8:])
		tool = rest[:strings.IndexByte(rest, '"')]
	}
	switch tool {
	case "svc_early":
		// Reply first, then read the rest of the request.
		_, _ = c.Write([]byte(`{"early":true}` + "\n"))
		_ = c.CloseWrite()
		data, _ := io.ReadAll(r)
		sum := sha256.Sum256(data)
		_ = os.WriteFile(filepath.Join(state, "early"), []byte(hex.EncodeToString(sum[:])), 0o600)
		return
	case "svc_late":
		_, _ = c.Write([]byte(`{"late":true}`))
		return
	}
	data, _ := io.ReadAll(r)
	var req struct {
		Tool      string          `json:"tool"`
		Agent     string          `json:"agent"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(data, &req)
	switch req.Tool {
	case "svc_echo":
		_, hasKey := os.LookupEnv("OPENROUTER_API_KEY")
		_, hasMine := os.LookupEnv("MY_KEY")
		out, _ := json.Marshal(map[string]any{"request": string(data), "pid": os.Getpid(), "dir": os.Getenv(extensions.EnvDir), "keys": hasKey || hasMine})
		_, _ = c.Write(out)
	case "svc_sum":
		sum := sha256.Sum256(req.Arguments)
		fmt.Fprintf(c, `{"len":%d,"sum":%q}`, len(req.Arguments), hex.EncodeToString(sum[:]))
	case "svc_big":
		var a struct{ N int }
		_ = json.Unmarshal(req.Arguments, &a)
		_, _ = c.Write([]byte(`"` + strings.Repeat("b", a.N) + `"`))
	case "svc_block":
		// Wait for the caller to go: writes fail once the connection closes.
		for {
			if _, err := c.Write([]byte(" ")); err != nil {
				_ = os.WriteFile(filepath.Join(state, "closed"), []byte("1"), 0o600)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	case "svc_slow":
		time.Sleep(600 * time.Millisecond)
		_, _ = c.Write([]byte(`{"slow":true}`))
	case "svc_fail":
		_, _ = c.Write([]byte(`{"error":"nope"}`))
	default:
		_, _ = c.Write([]byte(`{"unknown":true}`))
	}
}

// --- helpers for the tests ---

// shortDir is a temporary directory with a path short enough for sockets.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "fx")
	if err != nil {
		t.Fatal(err)
	}
	d, _ = filepath.EvalSymlinks(d)
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func self(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func writeSpec(t *testing.T, dir, name string, v any) {
	t.Helper()
	var data []byte
	if s, ok := v.(string); ok {
		data = []byte(s)
	} else {
		data, _ = json.Marshal(v)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type logs struct {
	ch chan string
}

func newLogs() *logs { return &logs{ch: make(chan string, 256)} }

func (l *logs) f(format string, args ...any) {
	select {
	case l.ch <- fmt.Sprintf(format, args...):
	default:
	}
}

func (l *logs) all() []string {
	var out []string
	for {
		select {
		case s := <-l.ch:
			out = append(out, s)
		default:
			return out
		}
	}
}

// fixture is a daemon directory with extensions.
type fixture struct {
	dir   string // daemon dir
	state string // where test services leave notes
	x     *extensions.Extensions
	logs  *logs
	eng   *engine.Engine // set by serve
}

func testEnv(state string) []string {
	return append(os.Environ(), "EXT_CHILD=1", "EXT_STATE="+state, "OPENROUTER_API_KEY=secret-one", "MY_KEY=secret-two", "PLAIN_VAR=plain", "GORACE=atexit_sleep_ms=0")
}

// open loads specs written into a fresh tools.d and starts the services.
func open(t *testing.T, specs map[string]any) *fixture {
	t.Helper()
	base := shortDir(t)
	f := &fixture{dir: filepath.Join(base, "d"), state: filepath.Join(base, "s"), logs: newLogs()}
	_ = os.MkdirAll(f.state, 0o700)
	_ = os.MkdirAll(f.dir, 0o700)
	cfg := filepath.Join(base, "config.toml")
	for name, v := range specs {
		writeSpec(t, filepath.Join(base, "tools.d"), name, v)
	}
	x, err := extensions.Open(context.Background(), extensions.Options{
		ConfigPath: cfg, DaemonDir: f.dir, KeyEnvs: []string{"MY_KEY"}, Env: testEnv(f.state), Log: f.logs.f,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.x = x
	t.Cleanup(x.Close)
	return f
}

func pluginSpec(t *testing.T, name string) map[string]any {
	return map[string]any{"name": name, "description": "A test plugin.", "entry": self(t)}
}

func serviceSpec(t *testing.T, name string, args ...string) map[string]any {
	obj := json.RawMessage(`{"type":"object"}`)
	tools := []map[string]any{}
	for _, n := range []string{"svc_echo", "svc_sum", "svc_big", "svc_block", "svc_early", "svc_late", "svc_fail", "svc_slow"} {
		tools = append(tools, map[string]any{"name": n, "description": n, "input_schema": obj})
	}
	tools = append(tools, map[string]any{"name": "svc_hidden", "description": "not for MCP", "input_schema": obj, "mcp": false})
	return map[string]any{"service": name, "program": self(t), "args": append([]string{"serve"}, args...), "tools": tools}
}

// waitFor polls cond up to d.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
