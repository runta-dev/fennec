package extensions_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/daemon"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
)

// actTool stands in for act: it takes the desk turn as its caller.
func actTool(ctx context.Context, call *engine.Call, _ json.RawMessage) (any, *protocol.Error) {
	release, err := call.Turn(ctx)
	if err != nil {
		return nil, engine.TurnError(err)
	}
	defer release()
	return map[string]any{"acted": true}, nil
}

// serve runs a daemon over f's extensions and returns its engine.
func serve(t *testing.T, f *fixture, runOff bool) *engine.Engine {
	t.Helper()
	eng := &engine.Engine{
		Turn:    &sched.Turn{Busy: 500 * time.Millisecond},
		Stopper: &sched.Stopper{},
		Input:   protocol.InputForeground,
		Impl:    map[string]engine.Tool{protocol.ToolAct: actTool},
		RunOff:  runOff,
		Ext:     f.x,
	}
	d := &daemon.Daemon{
		Paths: daemon.PathsIn(f.dir), Handler: eng, Stopper: eng.Stopper,
		Info:  mcpserver.Info{Name: "fennec-cua", Version: "test", IDPrefix: "mcp"},
		Ready: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	select {
	case <-d.Ready:
	case <-done:
		t.Fatal("the daemon did not start")
	}
	t.Cleanup(func() { cancel(); <-done })
	f.eng = eng
	return eng
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type client struct {
	t      *testing.T
	conn   net.Conn
	r      *bufio.Reader
	id     int
	agent  string
	caps   map[string]json.RawMessage
	closed bool
	eng    *engine.Engine
}

// dial opens an MCP session on the daemon at f.
func dial(t *testing.T, f *fixture) *client {
	t.Helper()
	conn, err := net.Dial("unix", daemon.PathsIn(f.dir).MCP)
	if err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, conn: conn, r: bufio.NewReader(conn), eng: f.eng}
	t.Cleanup(func() { _ = conn.Close() })
	res, perr := c.rpc("initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]string{"name": "tester"}})
	if perr != nil {
		t.Fatal(perr.Message)
	}
	var init struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	_ = json.Unmarshal(res, &init)
	c.caps = init.Capabilities
	return c
}

func (c *client) send(method string, params any) int {
	c.t.Helper()
	c.id++
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	if _, err := c.conn.Write(append(data, '\n')); err != nil {
		c.t.Fatal(err)
	}
	return c.id
}

func (c *client) notify(method string, params any) {
	c.t.Helper()
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if _, err := c.conn.Write(append(data, '\n')); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) read() (int, json.RawMessage, *rpcErr) {
	c.t.Helper()
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	var rep struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *rpcErr         `json:"error"`
	}
	if err := json.Unmarshal(line, &rep); err != nil {
		c.t.Fatalf("bad reply %.200s", line)
	}
	return rep.ID, rep.Result, rep.Error
}

func (c *client) rpc(method string, params any) (json.RawMessage, *rpcErr) {
	c.t.Helper()
	id := c.send(method, params)
	got, res, perr := c.read()
	if got != id {
		c.t.Fatalf("reply to %d, want %d", got, id)
	}
	return res, perr
}

type toolResult struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func (r toolResult) text() string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}

func decodeResult(t *testing.T, res json.RawMessage) toolResult {
	t.Helper()
	var r toolResult
	if err := json.Unmarshal(res, &r); err != nil {
		t.Fatalf("bad tool result %s", res)
	}
	return r
}

// call makes one tools/call and fails the test on a JSON-RPC error.
func (c *client) call(tool string, args any) toolResult {
	c.t.Helper()
	res, perr := c.rpc("tools/call", map[string]any{"name": tool, "arguments": args})
	if perr != nil {
		c.t.Fatalf("%s: %d %s", tool, perr.Code, perr.Message)
	}
	return decodeResult(c.t, res)
}

// tools checks that tools/list names only the REPL's tools, then answers
// the engine and extension tools the REPL reaches (fennec.tools()), which
// stay callable by name for internal callers.
func (c *client) tools() []mcpserver.Tool {
	c.t.Helper()
	res, perr := c.rpc("tools/list", map[string]any{})
	if perr != nil {
		c.t.Fatal(perr.Message)
	}
	var out struct {
		Tools []mcpserver.Tool `json:"tools"`
	}
	_ = json.Unmarshal(res, &out)
	if got := toolNames(out.Tools); got != strings.Join(protocol.MCPTools, ",") {
		c.t.Fatalf("tools/list = %s", got)
	}
	return c.eng.EngineTools()
}

func toolNames(ts []mcpserver.Tool) string {
	s := ""
	for i, t := range ts {
		if i > 0 {
			s += ","
		}
		s += t.Name
	}
	return s
}

// errorCode reads the engine's error code from a failed tool result.
func errorCode(t *testing.T, r toolResult) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.text()), &e); err != nil {
		t.Fatalf("not an error result: %s", r.text())
	}
	return e.Error.Code
}

func runReport(t *testing.T, r toolResult) protocol.RunResult {
	t.Helper()
	var rep protocol.RunResult
	if r.IsError {
		var wrap struct {
			Report protocol.RunResult `json:"report"`
		}
		if err := json.Unmarshal([]byte(r.text()), &wrap); err != nil {
			t.Fatalf("bad report %s", r.text())
		}
		return wrap.Report
	}
	if err := json.Unmarshal([]byte(r.text()), &rep); err != nil {
		t.Fatalf("bad report %s", r.text())
	}
	return rep
}

// rawCall sends a tools/call whose arguments are exactly args, unchanged.
func (c *client) rawCall(tool, args string) toolResult {
	c.t.Helper()
	c.id++
	line := `{"jsonrpc":"2.0","id":` + strconv.Itoa(c.id) + `,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}` + "\n"
	if _, err := c.conn.Write([]byte(line)); err != nil {
		c.t.Fatal(err)
	}
	_, res, perr := c.read()
	if perr != nil {
		c.t.Fatalf("%s: %s", tool, perr.Message)
	}
	return decodeResult(c.t, res)
}
