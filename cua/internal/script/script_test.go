package script

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/protocol"
)

type fakeHost struct {
	mu    sync.Mutex
	calls []string
	args  []map[string]any
	usage protocol.Usage
	input string
	tool  func(name string, args map[string]any) (any, *protocol.Error)
}

func (h *fakeHost) Tool(ctx context.Context, name string, raw json.RawMessage) (any, *protocol.Error) {
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	h.mu.Lock()
	h.calls = append(h.calls, name)
	h.args = append(h.args, args)
	h.mu.Unlock()
	if h.tool != nil {
		return h.tool(name, args)
	}
	return map[string]any{"ok": true}, nil
}

func (h *fakeHost) Model(ctx context.Context, op string, raw json.RawMessage) (any, *protocol.Error) {
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, "model."+op)
	h.args = append(h.args, args)
	if h.usage == nil {
		h.usage = protocol.Usage{}
	}
	u := h.usage["m1"]
	u.Calls++
	u.InputTokens += 10
	u.OutputTokens += 2
	h.usage["m1"] = u
	return map[string]any{"text": "a green button"}, nil
}

func (h *fakeHost) Usage() protocol.Usage {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := protocol.Usage{}
	for k, v := range h.usage {
		out[k] = v
	}
	return out
}

func (h *fakeHost) Input() string {
	if h.input == "" {
		return protocol.InputForeground
	}
	return h.input
}

func run(t *testing.T, h *fakeHost, code string, params map[string]any, timeoutS int) protocol.RunResult {
	t.Helper()
	return Run(context.Background(), h, protocol.RunRequest{Code: code, Params: params, TimeoutS: timeoutS})
}

func TestRunResult(t *testing.T) {
	h := &fakeHost{}
	r := run(t, h, `
type W = { title: string }
export async function main(p: { n: number }) {
  console.log("hello", { a: 1 })
  const w: W = { title: "x" }
  return { n: p.n * 2, title: w.title, empty: (await cua.observe({})).ok }
}`, map[string]any{"n": 21}, 0)
	if r.Error != nil {
		t.Fatalf("error %+v logs %s", r.Error, r.Logs)
	}
	got, _ := json.Marshal(r.Result)
	if string(got) != `{"empty":true,"n":42,"title":"x"}` {
		t.Errorf("result %s", got)
	}
	if r.Logs != "hello {\"a\":1}\n" {
		t.Errorf("logs %q", r.Logs)
	}
	if r.Usage != nil {
		t.Errorf("usage without model calls: %v", r.Usage)
	}
	// params default to {}
	r = run(t, h, `export function main(p) { return Object.keys(p).length }`, nil, 0)
	if r.Result != int64(0) {
		t.Errorf("default params %v (%T)", r.Result, r.Result)
	}
}

func TestRunUsage(t *testing.T) {
	h := &fakeHost{}
	r := run(t, h, `export async function main() { await cua.model.ask("what?"); await cua.model.ask("again"); return 1 }`, nil, 0)
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	if u := r.Usage["m1"]; u.Calls != 2 || u.InputTokens != 20 {
		t.Errorf("usage %+v", r.Usage)
	}
}

func TestRunInputMode(t *testing.T) {
	r := run(t, &fakeHost{input: protocol.InputBackground}, `export function main() { return cua.input }`, nil, 0)
	if r.Result != protocol.InputBackground {
		t.Errorf("input %v", r.Result)
	}
}

func TestRunErrorKinds(t *testing.T) {
	cases := []struct {
		code, kind, msg string
	}{
		{`export function main() { throw new Error("boom") }`, protocol.RunException, "boom"},
		{`export async function main() { await sleep(1); throw new Error("later") }`, protocol.RunException, "later"},
		{`export const x = 1`, protocol.RunNoMain, "no main"},
		{`export function main( {`, protocol.RunImport, ""},
		{`import fs from "node:fs"; export function main() { return fs }`, protocol.RunImport, "node:fs"},
		{`export function main() { Promise.reject(new Error("escaped")); return 1 }`, protocol.RunCrash, "escaped"},
	}
	for _, c := range cases {
		r := run(t, &fakeHost{}, c.code, nil, 0)
		if r.Error == nil || r.Error.Kind != c.kind || !strings.Contains(r.Error.Message, c.msg) {
			t.Errorf("%s: %+v", c.code, r.Error)
		}
	}
	// A handled rejection is not a crash.
	r := run(t, &fakeHost{}, `export async function main() { const p = Promise.reject(new Error("x")); await sleep(20); try { await p } catch { return "caught" } }`, nil, 0)
	if r.Error != nil || r.Result != "caught" {
		t.Errorf("handled rejection: %+v %v", r.Error, r.Result)
	}
	// A relative path is refused before running.
	r = Run(context.Background(), &fakeHost{}, protocol.RunRequest{Path: "rel/script.ts"})
	if r.Error == nil || !strings.Contains(r.Error.Message, "absolute") {
		t.Errorf("relative path: %+v", r.Error)
	}
}

func TestRunTimeoutKeepsLogs(t *testing.T) {
	start := time.Now()
	r := Run(context.Background(), &fakeHost{}, protocol.RunRequest{Code: `export function main() { console.log("before"); for (;;) {} }`, TimeoutS: 1})
	if r.Error == nil || r.Error.Kind != protocol.RunTimeout {
		t.Fatalf("error %+v", r.Error)
	}
	if !strings.Contains(r.Logs, "before") {
		t.Errorf("logs lost: %q", r.Logs)
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Errorf("report took %s", d)
	}
}

func TestTimeoutReportsWithLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	r := Run(ctx, &fakeHost{}, protocol.RunRequest{Code: `export async function main() { console.log("waiting"); await sleep(60000) }`})
	if r.Error == nil || r.Error.Kind != protocol.RunStopped || !strings.Contains(r.Logs, "waiting") {
		t.Errorf("stopped run: %+v %q", r.Error, r.Logs)
	}
}

func TestUsageSurvivesTimeout(t *testing.T) {
	r := Run(context.Background(), &fakeHost{}, protocol.RunRequest{Code: `export async function main() { await cua.model.ask("q"); for (;;) {} }`, TimeoutS: 1})
	if r.Error == nil || r.Error.Kind != protocol.RunTimeout || r.Usage["m1"].Calls != 1 {
		t.Errorf("%+v %+v", r.Error, r.Usage)
	}
}

func TestInputTargets(t *testing.T) {
	h := &fakeHost{}
	r := run(t, h, `export async function main() {
  await cua.key("cmd+s")
  await cua.type("hi", { app: "TextEdit" })
  await cua.click(10, 20, { window: "Doc", button: "right" })
}`, nil, 0)
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	steps := func(i int) map[string]any { return h.args[i]["steps"].([]any)[0].(map[string]any) }
	if s := steps(0); s["key"] != "cmd+s" || s["app"] != nil {
		t.Errorf("key step %v", s)
	}
	if s := steps(1); s["type"] != "hi" || s["app"] != "TextEdit" {
		t.Errorf("type step %v", s)
	}
	if s := steps(2); s["window"] != "Doc" || s["button"] != "right" {
		t.Errorf("click step %v", s)
	}
}

func TestKeyTypePassApp(t *testing.T) {
	h := &fakeHost{}
	run(t, h, `export async function main() { await cua.key("return", { app: "Notes", pid: 42 }) }`, nil, 0)
	s := h.args[0]["steps"].([]any)[0].(map[string]any)
	if s["app"] != "Notes" || s["pid"] != float64(42) {
		t.Errorf("step %v", s)
	}
}

func TestAskImagesAndUsage(t *testing.T) {
	h := &fakeHost{}
	r := run(t, h, `export async function main() {
  const png = new Uint8Array([137, 80, 78, 71]).buffer
  await cua.model.ask("none")
  await cua.model.ask("one", png)
  await cua.model.ask("two", [png, png])
}`, nil, 0)
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	counts := []int{}
	for i, c := range h.calls {
		if c == "model.ask" {
			imgs, _ := h.args[i]["images"].([]any)
			counts = append(counts, len(imgs))
		}
	}
	if len(counts) != 3 || counts[0] != 0 || counts[1] != 1 || counts[2] != 2 {
		t.Errorf("images per call %v", counts)
	}
	if h.args[1]["images"].([]any)[0] != "iVBORw==" {
		t.Errorf("not base64: %v", h.args[1]["images"])
	}
	if r.Usage["m1"].Calls != 3 {
		t.Errorf("usage %+v", r.Usage)
	}
}

func TestNoModelKeyInRun(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-secret")
	r := run(t, &fakeHost{}, `export function main() {
  return [typeof process, typeof require, typeof fetch, JSON.stringify(cua).includes("sk-")]
}`, nil, 0)
	got, _ := json.Marshal(r.Result)
	if string(got) != `["undefined","undefined","undefined",false]` {
		t.Errorf("the script reaches %s", got)
	}
}

func TestRunFromAbsolutePathWithImports(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.ts"), []byte(`export const twice = (n: number) => n * 2`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.ts")
	if err := os.WriteFile(main, []byte(`import { twice } from "./lib"; export function main() { return twice(4) }`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Run(context.Background(), &fakeHost{}, protocol.RunRequest{Path: main})
	if r.Error != nil || r.Result != int64(8) {
		t.Errorf("%+v %v", r.Error, r.Result)
	}
}

func TestLogsCappedAt32KiB(t *testing.T) {
	r := run(t, &fakeHost{}, `export function main() { for (let i = 0; i < 5000; i++) console.log("0123456789") }`, nil, 0)
	if len(r.Logs) > LogCap+64 || !strings.HasSuffix(r.Logs, "[logs cut at 32 KiB]") {
		t.Errorf("logs %d bytes, tail %q", len(r.Logs), r.Logs[len(r.Logs)-30:])
	}
}

func TestToolErrorsRejectWithCode(t *testing.T) {
	h := &fakeHost{tool: func(string, map[string]any) (any, *protocol.Error) {
		return nil, &protocol.Error{Code: protocol.ErrTargetNotFound, Message: "no Save", Hint: "try Save…"}
	}}
	r := run(t, h, `export async function main() { try { await cua.press('button "Save"') } catch (e) { return [e.code, e.hint] } }`, nil, 0)
	got, _ := json.Marshal(r.Result)
	if string(got) != `["target_not_found","try Save…"]` {
		t.Errorf("%s %+v", got, r.Error)
	}
}
