package extensions_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/daemon"
	"fennec/cua/internal/extensions"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/schema"
)

var builtins = strings.Join(protocol.Tools, ",")

// CAP-DMN-037: plugins are listed after the six tools with run's input
// schema; without the plugin its name is an unknown tool.
func TestPluginListed(t *testing.T) {
	f := open(t, map[string]any{"b.json": pluginSpec(t, "bee_run"), "a.json": pluginSpec(t, "ay_run")})
	serve(t, f, false)
	c := dial(t, f)
	ts := c.tools()
	if got := toolNames(ts); got != builtins+",ay_run,bee_run" {
		t.Fatalf("tools = %s", got)
	}
	run := string(schema.For(protocol.RunRequest{}))
	for _, tl := range ts[len(protocol.Tools):] {
		if string(tl.InputSchema) != run || tl.Description != "A test plugin." {
			t.Errorf("%s: schema %s", tl.Name, tl.InputSchema)
		}
	}
	_, perr := c.rpc("tools/call", map[string]any{"name": "cee_run", "arguments": map[string]any{}})
	if perr == nil || perr.Code != mcpserver.CodeInvalidParams {
		t.Fatalf("an absent plugin must be an unknown tool, got %+v", perr)
	}
}

// CAP-DMN-037: a plugin runs under run's contract with its own entry; its
// desk calls come back through the daemon's socket as the run's caller and
// pass the engine's checks.
func TestPluginRuns(t *testing.T) {
	f := open(t, map[string]any{"p.json": pluginSpec(t, "test_run")})
	eng := serve(t, f, false)
	c := dial(t, f)

	r := c.call("test_run", map[string]any{"code": "main()", "params": map[string]any{"mode": "echo", "n": 1}})
	if r.IsError {
		t.Fatalf("echo failed: %s", r.text())
	}
	rep := runReport(t, r)
	var got struct {
		Params   map[string]any    `json:"params"`
		Code     string            `json:"code"`
		Env      map[string]string `json:"env"`
		RunToken bool              `json:"run_token"`
	}
	data, _ := json.Marshal(rep.Result)
	_ = json.Unmarshal(data, &got)
	if got.Code != "main()" || got.Params["n"] != float64(1) || !got.RunToken {
		t.Errorf("request did not arrive: %+v", got)
	}
	if !strings.Contains(rep.Logs, "hello from the plugin") || rep.Usage["m"].Calls != 1 {
		t.Errorf("logs %q usage %+v", rep.Logs, rep.Usage)
	}
	if got.Env[extensions.EnvPlugin] != "test_run" || !strings.HasPrefix(got.Env[extensions.EnvAgent], "mcp-tester-") || got.Env[extensions.EnvInput] != protocol.InputForeground || got.Env[extensions.EnvReportFD] != "3" {
		t.Errorf("env = %+v", got.Env)
	}

	// The plugin's own act passes as the run's caller: it re-enters the
	// desk turn the run holds instead of waiting for it.
	r = c.call("test_run", map[string]any{"params": map[string]any{"mode": "desk"}})
	if r.IsError || !strings.Contains(r.text(), `"acted":true`) {
		t.Fatalf("desk call through the daemon: %s", r.text())
	}
	// Another agent's act waits for the run's turn and gets busy.
	release, err := eng.Turn.Acquire(context.Background(), "someone-else")
	if err != nil {
		t.Fatal(err)
	}
	r = c.call("test_run", map[string]any{"params": map[string]any{"mode": "echo"}})
	if !r.IsError || errorCode(t, r) != string(protocol.ErrBusy) {
		t.Errorf("a plugin run must wait for the desk turn: %s", r.text())
	}
	release()

	// A reported error is a run failure with the report.
	r = c.call("test_run", map[string]any{"params": map[string]any{"mode": "error"}})
	if !r.IsError || errorCode(t, r) != string(protocol.ErrRunFailed) || runReport(t, r).Error.Message != "boom" {
		t.Errorf("error mode: %s", r.text())
	}
	// A stop refuses plugin runs.
	eng.Stopper.Stop()
	r = c.call("test_run", map[string]any{"params": map[string]any{"mode": "echo"}})
	if !r.IsError || errorCode(t, r) != string(protocol.ErrStopped) {
		t.Errorf("stopped: %s", r.text())
	}
	eng.Stopper.Resume()
}

// CAP-DMN-037: plugin resources through resources/list and resources/read;
// the capability is named only when there are resources.
func TestPluginResources(t *testing.T) {
	base := shortDir(t)
	doc := filepath.Join(base, "GUIDE.md")
	if err := os.WriteFile(doc, []byte("# Guide\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := pluginSpec(t, "doc_run")
	spec["resources"] = []any{map[string]any{"uri": "doc://GUIDE.md", "path": doc, "mime": "text/markdown", "description": "The guide"}}
	f := open(t, map[string]any{"p.json": spec})
	serve(t, f, false)
	c := dial(t, f)
	if _, ok := c.caps["resources"]; !ok {
		t.Fatalf("capabilities %v name no resources", c.caps)
	}
	res, perr := c.rpc("resources/list", map[string]any{})
	if perr != nil || !strings.Contains(string(res), `{"uri":"doc://GUIDE.md","name":"GUIDE.md","description":"The guide","mimeType":"text/markdown"}`) {
		t.Fatalf("resources/list = %s %+v", res, perr)
	}
	res, perr = c.rpc("resources/read", map[string]any{"uri": "doc://GUIDE.md"})
	if perr != nil || !strings.Contains(string(res), `"text":"# Guide\n"`) || !strings.Contains(string(res), `"mimeType":"text/markdown"`) {
		t.Fatalf("resources/read = %s %+v", res, perr)
	}
	if _, perr = c.rpc("resources/read", map[string]any{"uri": "doc://nope"}); perr == nil || perr.Code != -32602 {
		t.Fatalf("an unknown uri must answer -32602, got %+v", perr)
	}

	// Without resources the capability is not named.
	g := open(t, map[string]any{"p.json": pluginSpec(t, "plain_run")})
	serve(t, g, false)
	if _, ok := dial(t, g).caps["resources"]; ok {
		t.Error("resources capability without resources")
	}
}

// CAP-RGB-012: a spec file adds a *_run tool that runs its own program
// under the run contract: params in, result, logs and error kinds out.
func TestPluginSpecAddsRunTool(t *testing.T) {
	f := open(t, map[string]any{"p.json": pluginSpec(t, "demo_run")})
	if !f.x.IsPlugin("demo_run") || f.x.IsPlugin("demo") {
		t.Fatal("demo_run is not a plugin tool")
	}
	ctx := context.Background()
	rep := f.x.RunPlugin(ctx, "demo_run", extensions.RunCall{Agent: "a1"}, protocol.RunRequest{Params: map[string]any{"mode": "echo", "k": "v"}})
	if rep.Error != nil || !strings.Contains(rep.Logs, "hello") || rep.Result.(map[string]any)["params"].(map[string]any)["k"] != "v" {
		t.Fatalf("echo = %+v", rep)
	}
	rep = f.x.RunPlugin(ctx, "demo_run", extensions.RunCall{Agent: "a1"}, protocol.RunRequest{Params: map[string]any{"mode": "error"}})
	if rep.Error == nil || rep.Error.Kind != protocol.RunException || rep.Error.Message != "boom" {
		t.Fatalf("error = %+v", rep.Error)
	}
	start := time.Now()
	rep = f.x.RunPlugin(ctx, "demo_run", extensions.RunCall{Agent: "a1"}, protocol.RunRequest{TimeoutS: 1, Params: map[string]any{"mode": "term"}})
	if rep.Error == nil || rep.Error.Kind != protocol.RunTimeout || !strings.Contains(rep.Logs, "cleaned up") || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout = %+v after %s", rep, time.Since(start))
	}
	// The run token lives only while the run does.
	if _, ok := f.x.RunOwner("anything"); ok {
		t.Error("a stray token owns a run")
	}
}

// CAP-RGB-012: turning scripts off turns plugins off: neither listed nor
// callable, and their resources go with them.
func TestScriptsOffDisablesPlugins(t *testing.T) {
	base := shortDir(t)
	doc := filepath.Join(base, "r.txt")
	_ = os.WriteFile(doc, []byte("r"), 0o600)
	spec := pluginSpec(t, "off_run")
	spec["resources"] = []any{map[string]any{"uri": "doc://r", "path": doc, "mime": "text/plain", "description": "r"}}
	f := open(t, map[string]any{"p.json": spec})
	serve(t, f, true)
	c := dial(t, f)
	if got := toolNames(c.tools()); got != builtins {
		t.Fatalf("tools with scripts off = %s", got)
	}
	if _, perr := c.rpc("tools/call", map[string]any{"name": "off_run", "arguments": map[string]any{}}); perr == nil || perr.Code != mcpserver.CodeInvalidParams {
		t.Fatalf("a plugin with scripts off must be unknown, got %+v", perr)
	}
	if _, ok := c.caps["resources"]; ok {
		t.Error("resources named with scripts off")
	}
	if _, perr := c.rpc("resources/read", map[string]any{"uri": "doc://r"}); perr == nil || perr.Code != -32602 {
		t.Errorf("resource readable with scripts off: %+v", perr)
	}
}

// CAP-RGB-026: a timeout sends SIGTERM, then SIGKILL 2 s later; the report
// arrives within about 2.5 s of the timeout with the logs kept, and a
// caller that leaves during the grace ends it at once.
func TestTermThenKill(t *testing.T) {
	f := open(t, map[string]any{"p.json": pluginSpec(t, "stub_run")})
	start := time.Now()
	rep := f.x.RunPlugin(context.Background(), "stub_run", extensions.RunCall{}, protocol.RunRequest{TimeoutS: 1, Params: map[string]any{"mode": "ignore_term"}})
	took := time.Since(start)
	if rep.Error == nil || rep.Error.Kind != protocol.RunTimeout || !strings.Contains(rep.Logs, "stubborn") {
		t.Fatalf("report = %+v", rep)
	}
	if took < 2900*time.Millisecond || took > 3500*time.Millisecond {
		t.Errorf("timeout 1 s plus a 2 s grace took %s", took)
	}

	// A stop (the call's context ends) gets the same grace and reports
	// stopped.
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start = time.Now()
	rep = f.x.RunPlugin(ctx, "stub_run", extensions.RunCall{}, protocol.RunRequest{Params: map[string]any{"mode": "term"}})
	if rep.Error == nil || rep.Error.Kind != protocol.RunStopped || !strings.Contains(rep.Logs, "cleaned up") || time.Since(start) > time.Second {
		t.Fatalf("stopped report = %+v after %s", rep, time.Since(start))
	}

	// The caller leaves during a timeout's grace: the plugin ends at once.
	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(1300*time.Millisecond, cancel)
	start = time.Now()
	rep = f.x.RunPlugin(ctx, "stub_run", extensions.RunCall{}, protocol.RunRequest{TimeoutS: 1, Params: map[string]any{"mode": "ignore_term"}})
	if took := time.Since(start); rep.Error == nil || rep.Error.Kind != protocol.RunTimeout || took > 1800*time.Millisecond {
		t.Fatalf("leaving during the grace: %+v after %s", rep.Error, took)
	}
}

// CAP-RGB-068: a plugin that exits nonzero without reporting an error names
// the last 2 KiB of its stderr.
func TestCrashStderrTail(t *testing.T) {
	f := open(t, map[string]any{"p.json": pluginSpec(t, "crash_run")})
	rep := f.x.RunPlugin(context.Background(), "crash_run", extensions.RunCall{}, protocol.RunRequest{Params: map[string]any{"mode": "crash"}})
	if rep.Error == nil || rep.Error.Kind != protocol.RunException {
		t.Fatalf("report = %+v", rep)
	}
	msg := rep.Error.Message
	if !strings.Contains(msg, "exited with status 3") || !strings.HasSuffix(msg, "TAILMARK the end") {
		t.Errorf("message does not end with the stderr tail: %.200q", msg)
	}
	if n := strings.Count(msg, "x"); n > extensions.StderrTail || n < extensions.StderrTail-100 {
		t.Errorf("the tail holds %d bytes of the 5000 written, want about 2 KiB", n)
	}
}

// CAP-RGB-019: plugins and services get the daemon's endpoint in
// FENNEC_CUA_DIR, the variable fennec-cua itself reads to find the daemon,
// and none of the model key variables.
func TestEndpointEnv(t *testing.T) {
	f := open(t, map[string]any{"p.json": pluginSpec(t, "env_run"), "s.json": serviceSpec(t, "envsvc")})
	rep := f.x.RunPlugin(context.Background(), "env_run", extensions.RunCall{Agent: "a"}, protocol.RunRequest{Params: map[string]any{"mode": "echo"}})
	env := rep.Result.(map[string]any)["env"].(map[string]any)
	if env[extensions.EnvDir] != f.dir || env[extensions.EnvEmbedded] != "1" {
		t.Errorf("plugin env = %v", env)
	}
	if _, ok := env["OPENROUTER_API_KEY"]; ok {
		t.Error("the plugin sees the default model key")
	}
	if _, ok := env["MY_KEY"]; ok {
		t.Error("the plugin sees the configured model key")
	}
	if env["PLAIN_VAR"] != "plain" {
		t.Error("other variables must pass")
	}
	reply, err := f.x.CallService(context.Background(), "svc_echo", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Dir  string `json:"dir"`
		Keys bool   `json:"keys"`
	}
	_ = json.Unmarshal(reply, &got)
	if got.Dir != f.dir || got.Keys {
		t.Errorf("service env: %s", reply)
	}
	t.Setenv(extensions.EnvDir, f.dir)
	p, err := daemon.DefaultPaths()
	if err != nil || p.Dir != f.dir || p.MCP != filepath.Join(f.dir, "cua.sock") {
		t.Errorf("the binary reads another variable: %+v %v", p, err)
	}
}
