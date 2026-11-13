package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fennec/cua/internal/cli"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/protocol"
)

func TestStartsOnFirstNonStatusCall(t *testing.T) {
	p := tempPaths(t)
	eng := newEngine(map[string]engine.Tool{
		protocol.ToolObserve: func(_ context.Context, call *engine.Call, _ json.RawMessage) (any, *protocol.Error) {
			return map[string]string{"from": "daemon", "agent": call.Session.AgentID}, nil
		},
	})
	eng.plain = true // the production surface: js and js_reset only
	local := newEngine(nil)
	local.plain = true
	var starts atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	t.Cleanup(func() {
		cancel()
		if starts.Load() > 0 {
			<-errc
		}
	})
	start := func() (<-chan error, error) {
		starts.Add(1)
		d := &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper, Info: mcpserver.Info{Name: "t", IDPrefix: "mcp"}}
		go func() { errc <- d.Run(ctx) }()
		return errc, nil
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- mcpserver.Bridge(context.Background(), inR, outW, mcpserver.BridgeConfig{
			Socket: p.MCP,
			Start:  start,
			Local:  local,
			Info:   mcpserver.Info{Name: "fennec-cua", IDPrefix: "mcp"},
		})
		_ = outW.Close()
	}()
	out := bufio.NewScanner(outR)
	out.Buffer(make([]byte, 1<<20), 1<<20)
	rpc := func(line string) map[string]any {
		t.Helper()
		if _, err := inW.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
		if !out.Scan() {
			t.Fatal("no reply")
		}
		var m map[string]any
		_ = json.Unmarshal(out.Bytes(), &m)
		return m
	}
	if m := rpc(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"agent"}}}`); m["result"] == nil {
		t.Fatalf("initialize %v", m)
	}
	_, _ = inW.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	if m := rpc(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); len(m["result"].(map[string]any)["tools"].([]any)) != 2 {
		t.Fatalf("tools/list %v", m)
	}
	// An engine tool by name is unknown, answered here without a daemon.
	if m := rpc(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status"}}`); m["error"] == nil {
		t.Fatalf("status by name %v", m)
	}
	if starts.Load() != 0 {
		t.Fatalf("initialize, tools/list or status started the daemon (%d)", starts.Load())
	}
	js := func(id int) string {
		t.Helper()
		m := rpc(`{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"tools/call","params":{"name":"js","arguments":{"code":"nodeRepl.write(JSON.stringify(await fennec.call(\"observe\", {})))"}}}`)
		var parts []string
		for _, c := range m["result"].(map[string]any)["content"].([]any) {
			parts = append(parts, c.(map[string]any)["text"].(string))
		}
		return strings.Join(parts, "\n")
	}
	text := js(4)
	if starts.Load() != 1 || !strings.Contains(text, `"from":"daemon"`) {
		t.Fatalf("first call: %s (starts %d)", text, starts.Load())
	}
	// The daemon saw the client's own handshake.
	if !strings.Contains(text, `"agent":"mcp-agent-`) {
		t.Errorf("handshake not replayed: %s", text)
	}
	// Later calls go to the running daemon.
	js(5)
	if m := rpc(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"status"}}`); m["id"] != 6.0 || m["error"] == nil {
		t.Errorf("status %v", m)
	}
	if starts.Load() != 1 {
		t.Errorf("started %d times", starts.Load())
	}
	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the bridge outlived its stdin")
	}
}

func TestCLIDoRoundTrip(t *testing.T) {
	eng := newEngine(map[string]engine.Tool{
		protocol.ToolObserve: func(_ context.Context, _ *engine.Call, args json.RawMessage) (any, *protocol.Error) {
			var req protocol.ObserveRequest
			if perr := engine.Decode(args, &req); perr != nil {
				return nil, perr
			}
			return map[string]any{"scope": req.Scope, "nodes": req.Nodes}, nil
		},
	})
	// The production surface: fennec-cua do reaches the engine through js.
	eng.plain = true
	p, _, _ := startDaemon(t, eng)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, argv := range [][]string{{`{"scope":"desktop","nodes":true}`}, {"scope=desktop", "nodes=true"}} {
		args, err := cli.ParseDoArgs(argv)
		if err != nil {
			t.Fatal(err)
		}
		r, err := cli.Do(ctx, p.MCP, "observe", args)
		if err != nil || r.IsError || !strings.Contains(r.Text(), `"scope":"desktop"`) || !strings.Contains(r.Text(), `"nodes":true`) {
			t.Errorf("%v: %s %v", argv, r.Text(), err)
		}
	}
	r, err := cli.Do(ctx, p.MCP, "status", nil)
	if err != nil || !strings.Contains(r.Text(), `"version"`) {
		t.Errorf("status: %s %v", r.Text(), err)
	}
	r, _ = cli.Do(ctx, p.MCP, "observe", json.RawMessage(`{"bogus":1}`))
	if !r.IsError || !strings.Contains(r.Text(), "invalid_argument") {
		t.Errorf("bad argument: %s", r.Text())
	}
}
