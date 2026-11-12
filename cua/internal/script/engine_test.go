package script_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/engine"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
)

func newEngine(runOff bool) *engine.Engine {
	e := &engine.Engine{Turn: &sched.Turn{Busy: 300 * time.Millisecond}, Stopper: &sched.Stopper{}, RunOff: runOff}
	e.Impl = map[string]engine.Tool{
		protocol.ToolObserve: func(context.Context, *engine.Call, json.RawMessage) (any, *protocol.Error) {
			return map[string]any{"view": "v1"}, nil
		},
		protocol.ToolAct: func(ctx context.Context, call *engine.Call, _ json.RawMessage) (any, *protocol.Error) {
			release, err := call.Turn(ctx)
			if err != nil {
				return nil, engine.TurnError(err)
			}
			release()
			return map[string]any{"ok": true}, nil
		},
	}
	e.Impl[protocol.ToolRun] = e.RunTool(nil)
	return e
}

func call(e *engine.Engine, agent, tool string, args any) mcpserver.Result {
	data, _ := json.Marshal(args)
	return e.Call(context.Background(), &mcpserver.Session{AgentID: agent}, tool, data)
}

func TestRunTurnedOff(t *testing.T) {
	e := newEngine(true)
	r := call(e, "a", "run", map[string]any{"code": "export function main() { return 1 }"})
	if !r.IsError || !strings.Contains(r.Text, "[tools]") {
		t.Errorf("run while off: %s", r.Text)
	}
	if r := call(e, "a", "observe", map[string]any{}); r.IsError {
		t.Errorf("observe while scripts are off: %s", r.Text)
	}
}

func TestErrorReportIsToolError(t *testing.T) {
	e := newEngine(false)
	r := call(e, "a", "run", map[string]any{"code": `export function main() { console.log("trace me"); throw new Error("nope") }`})
	if !r.IsError || !strings.Contains(r.Text, "trace me") || !strings.Contains(r.Text, `"kind":"exception"`) {
		t.Errorf("failed run: %s", r.Text)
	}
	ok := call(e, "a", "run", map[string]any{"code": `export async function main() { return (await cua.act({steps: []})).ok }`})
	if ok.IsError || !strings.Contains(ok.Text, `"result":true`) {
		t.Errorf("run's own act re-enters its turn: %s", ok.Text)
	}
}

func TestTwoRunsNeverInterleave(t *testing.T) {
	e := newEngine(false)
	started := make(chan struct{})
	done := make(chan mcpserver.Result)
	go func() {
		close(started)
		done <- call(e, "a", "run", map[string]any{"code": `export async function main() { await sleep(600); return 1 }`})
	}()
	<-started
	time.Sleep(50 * time.Millisecond)
	r := call(e, "b", "act", map[string]any{"steps": []any{}})
	if !r.IsError || !strings.Contains(r.Text, `"busy"`) {
		t.Errorf("another agent's act during a run: %s", r.Text)
	}
	<-done
}
