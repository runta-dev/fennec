package extensions_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"fennec/cua/internal/extensions"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
)

var mcpServiceTools = "svc_echo,svc_sum,svc_big,svc_block,svc_early,svc_late,svc_fail,svc_slow"

// started waits for a service's process and its first answer.
func started(t *testing.T, f *fixture, service string) int {
	t.Helper()
	var pid int
	waitFor(t, 5*time.Second, service+" to start", func() bool {
		pid = f.x.ServicePID(service)
		return pid > 0
	})
	if _, err := f.x.CallService(context.Background(), "svc_echo", "t", nil); err != nil {
		t.Fatalf("%s does not answer: %v", service, err)
	}
	return pid
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// CAP-DMN-040: service tools are listed after the plugins with their own
// schema; tools marked mcp false are neither listed nor callable.
func TestServiceListed(t *testing.T) {
	spec := serviceSpec(t, "lst")
	spec["tools"] = append(spec["tools"].([]map[string]any), map[string]any{"name": "svc_props", "description": "with properties", "input_schema": json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`)})
	f := open(t, map[string]any{"a.json": spec, "b.json": pluginSpec(t, "zed_run")})
	serve(t, f, false)
	c := dial(t, f)
	ts := c.tools()
	if got := toolNames(ts); got != builtins+",zed_run,"+mcpServiceTools+",svc_props" {
		t.Fatalf("tools = %s", got)
	}
	if last := ts[len(ts)-1]; string(last.InputSchema) != `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}` || last.Description != "with properties" {
		t.Errorf("schema = %s", last.InputSchema)
	}
	if _, perr := c.rpc("tools/call", map[string]any{"name": "svc_hidden", "arguments": map[string]any{}}); perr == nil || perr.Code != mcpserver.CodeInvalidParams {
		t.Fatalf("an mcp false tool must not be callable, got %+v", perr)
	}
	if f.x.IsService("svc_hidden") || !f.x.IsService("svc_echo") {
		t.Error("IsService")
	}
}

// CAP-DMN-040: a call reaches the service unmodified with the caller's
// agent id, and the reply comes back unchanged; a service call takes no desk
// turn.
func TestServiceForward(t *testing.T) {
	f := open(t, map[string]any{"s.json": serviceSpec(t, "fwd")})
	eng := serve(t, f, false)
	started(t, f, "fwd")
	c := dial(t, f)
	// Another agent holds the desk turn: service calls do not wait for it.
	release, err := eng.Turn.Acquire(context.Background(), "someone-else")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	args := `{"b": [1, 2.50, "x"], "a": {"z": null}}`
	start := time.Now()
	r := c.rawCall("svc_echo", args)
	if time.Since(start) > 400*time.Millisecond {
		t.Errorf("the call waited for the desk turn")
	}
	var got struct {
		Request string `json:"request"`
		Pid     int    `json:"pid"`
	}
	if err := json.Unmarshal([]byte(r.text()), &got); err != nil || r.IsError {
		t.Fatalf("reply %s", r.text())
	}
	var req struct {
		Tool      string          `json:"tool"`
		Agent     string          `json:"agent"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(got.Request), &req); err != nil {
		t.Fatalf("request %q", got.Request)
	}
	if req.Tool != "svc_echo" || !strings.HasPrefix(req.Agent, "mcp-tester-") || string(req.Arguments) != args {
		t.Errorf("forwarded %q", got.Request)
	}
	// The reply is the service's bytes as they were.
	reply, err := f.x.CallService(context.Background(), "svc_fail", "a", nil)
	if err != nil || string(reply) != `{"error":"nope"}` {
		t.Fatalf("reply %q %v", reply, err)
	}
	if r := c.call("svc_fail", map[string]any{}); !r.IsError || r.text() != `{"error":"nope"}` {
		t.Errorf("an error reply = %+v", r)
	}
}

// CAP-DMN-040: 64 MiB pass both ways intact; a service that replies before
// reading the whole request still receives all of it.
func TestServiceLargePayload(t *testing.T) {
	f := open(t, map[string]any{"s.json": serviceSpec(t, "big")})
	started(t, f, "big")
	ctx := context.Background()
	payload := make([]byte, 0, 64<<20)
	payload = append(payload, `{"blob":"`...)
	for i := 0; len(payload) < 64<<20-16; i++ {
		payload = append(payload, "abcdefghijklmnopqrstuvwxyz0123456789"[i%36])
	}
	payload = append(payload, `"}`...)
	sum := sha256.Sum256(payload)
	reply, err := f.x.CallService(ctx, "svc_sum", "a", payload)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"len":` + strconv.Itoa(len(payload)) + `,"sum":"` + hex.EncodeToString(sum[:]) + `"}`; string(reply) != want {
		t.Fatalf("service saw %s, want %s", reply, want)
	}
	n := 64<<20 - 2
	reply, err = f.x.CallService(ctx, "svc_big", "a", json.RawMessage(`{"n":`+strconv.Itoa(n)+`}`))
	if err != nil || len(reply) != n+2 || strings.Count(string(reply), "b") != n {
		t.Fatalf("a 64 MiB reply arrived with %d bytes: %v", len(reply), err)
	}
	reply, err = f.x.CallService(ctx, "svc_early", "a", payload)
	if err != nil || string(reply) != `{"early":true}` {
		t.Fatalf("early reply %q %v", reply, err)
	}
	waitFor(t, 5*time.Second, "the early service's note", func() bool { return exists(filepath.Join(f.state, "early")) })
	note, _ := os.ReadFile(filepath.Join(f.state, "early"))
	head := `{"tool":"svc_early","agent":"a","arguments":`
	whole := sha256.Sum256(append(append([]byte(head), payload...), "}\n"...))
	// The service peeked the head before replying and read the rest after.
	if string(note) != hex.EncodeToString(whole[:]) {
		t.Errorf("the service that replied early did not get the whole request")
	}
}

// CAP-RGB-014: each call's bytes go to the service without the desk turn
// or a deadline (the listen wait bounds only the connect).
func TestServiceSplice(t *testing.T) {
	f := open(t, map[string]any{"s.json": serviceSpec(t, "spl")})
	eng := serve(t, f, false)
	started(t, f, "spl")
	f.x.ListenWait = 200 * time.Millisecond
	release, _ := eng.Turn.Acquire(context.Background(), "holder")
	defer release()
	c := dial(t, f)
	r := c.call("svc_slow", map[string]any{})
	if r.IsError || r.text() != `{"slow":true}` {
		t.Fatalf("slow call: %s", r.text())
	}
	// Escapes and spacing arrive as the client sent them.
	args := `{"x": "é\n" }`
	sum := sha256.Sum256([]byte(args))
	r = c.rawCall("svc_sum", args)
	if r.text() != `{"len":`+strconv.Itoa(len(args))+`,"sum":"`+hex.EncodeToString(sum[:])+`"}` {
		t.Errorf("bytes changed on the way: %s", r.text())
	}
}

// CAP-DMN-041: a client that cancels or disconnects ends the service call
// and the service sees its connection close.
func TestServiceCancel(t *testing.T) {
	f := open(t, map[string]any{"s.json": serviceSpec(t, "cnl")})
	serve(t, f, false)
	started(t, f, "cnl")
	closed := filepath.Join(f.state, "closed")

	c := dial(t, f)
	id := c.send("tools/call", map[string]any{"name": "svc_block", "arguments": map[string]any{}})
	time.Sleep(200 * time.Millisecond)
	c.notify("notifications/cancelled", map[string]any{"requestId": id})
	waitFor(t, 2*time.Second, "the service to see the cancel", func() bool { return exists(closed) })
	// The cancelled call gets no reply; the session goes on.
	if r := c.call("svc_echo", map[string]any{}); r.IsError {
		t.Fatalf("after cancel: %s", r.text())
	}
	_ = os.Remove(closed)

	d := dial(t, f)
	d.send("tools/call", map[string]any{"name": "svc_block", "arguments": map[string]any{}})
	time.Sleep(200 * time.Millisecond)
	_ = d.conn.Close()
	waitFor(t, 2*time.Second, "the service to see the disconnect", func() bool { return exists(closed) })
}

// CAP-DMN-041: a reply the service sends just before closing reaches the
// caller even when the caller is still sending.
func TestServiceLateReply(t *testing.T) {
	f := open(t, map[string]any{"s.json": serviceSpec(t, "late")})
	started(t, f, "late")
	big := json.RawMessage(`"` + strings.Repeat("q", 8<<20) + `"`)
	for i := 0; i < 5; i++ {
		reply, err := f.x.CallService(context.Background(), "svc_late", "a", big)
		if err != nil || string(reply) != `{"late":true}` {
			t.Fatalf("late reply %q %v", reply, err)
		}
	}
}

// CAP-DMN-041: a call no service accepts within the listen wait (10 s by
// default) fails with an error; a service still starting is waited for.
func TestServiceListenWait(t *testing.T) {
	if extensions.New(extensions.Set{}, extensions.Options{}).ListenWait != 10*time.Second {
		t.Fatal("the listen wait must be 10 s")
	}
	spec := serviceSpec(t, "deaf")
	spec["program"] = "/usr/bin/true" // exits at once and never listens
	f := open(t, map[string]any{"s.json": spec})
	f.x.ListenWait = 300 * time.Millisecond
	start := time.Now()
	_, err := f.x.CallService(context.Background(), "svc_echo", "a", nil)
	if !errors.Is(err, extensions.ErrNoService) || !strings.Contains(err.Error(), "deaf") {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took < 300*time.Millisecond || took > time.Second {
		t.Errorf("gave up after %s", took)
	}
	// A call made as the daemon starts waits for the service to listen.
	g := open(t, map[string]any{"s.json": serviceSpec(t, "slowstart")})
	if _, err := g.x.CallService(context.Background(), "svc_echo", "a", nil); err != nil {
		t.Fatalf("a call during the start: %v", err)
	}
}

// stopFixture serves a service and starts n blocking calls.
func blockCalls(t *testing.T, f *fixture, n int) []chan toolResult {
	var out []chan toolResult
	for i := 0; i < n; i++ {
		c := dial(t, f)
		ch := make(chan toolResult, 1)
		c.send("tools/call", map[string]any{"name": "svc_block", "arguments": map[string]any{}})
		go func() {
			_, res, _ := c.read()
			var r toolResult
			_ = json.Unmarshal(res, &r)
			ch <- r
		}()
		out = append(out, ch)
	}
	time.Sleep(300 * time.Millisecond)
	return out
}

// CAP-DMN-042: a stop closes calls in flight within 1 s and refuses new
// ones, but the service keeps running; resume lets calls through again.
func TestServiceStop(t *testing.T) {
	f := open(t, map[string]any{"s.json": serviceSpec(t, "stp")})
	eng := serve(t, f, false)
	pid := started(t, f, "stp")
	calls := blockCalls(t, f, 1)
	eng.Stopper.Stop()
	select {
	case r := <-calls[0]:
		if !r.IsError || errorCode(t, r) != string(protocol.ErrStopped) {
			t.Errorf("in flight: %s", r.text())
		}
	case <-time.After(time.Second):
		t.Fatal("the call in flight outlived the stop by 1 s")
	}
	waitFor(t, time.Second, "the service to see the close", func() bool { return exists(filepath.Join(f.state, "closed")) })
	c := dial(t, f)
	if r := c.call("svc_echo", map[string]any{}); !r.IsError || errorCode(t, r) != string(protocol.ErrStopped) || !strings.Contains(r.text(), sched.ErrStopped.Error()[:20]) {
		t.Errorf("a new call while stopped: %s", r.text())
	}
	eng.Stopper.Resume()
	r := c.call("svc_echo", map[string]any{})
	if r.IsError || !strings.Contains(r.text(), `"pid":`+strconv.Itoa(pid)) || f.x.ServicePID("stp") != pid {
		t.Errorf("after resume: %s (want pid %d)", r.text(), pid)
	}
}

// CAP-RGB-014: a stop closes every call in flight while the service keeps
// running.
func TestStopClosesServiceCalls(t *testing.T) {
	f := open(t, map[string]any{"s.json": serviceSpec(t, "many")})
	eng := serve(t, f, false)
	pid := started(t, f, "many")
	calls := blockCalls(t, f, 3)
	start := time.Now()
	eng.Stopper.Stop()
	for i, ch := range calls {
		select {
		case r := <-ch:
			if errorCode(t, r) != string(protocol.ErrStopped) {
				t.Errorf("call %d: %s", i, r.text())
			}
		case <-time.After(time.Second - time.Since(start)):
			t.Fatalf("call %d still open 1 s after the stop", i)
		}
	}
	if f.x.ServicePID("many") != pid || !alive(pid) {
		t.Error("the stop touched the service")
	}
	eng.Stopper.Resume()
}

// CAP-DMN-042: turning scripts off leaves services running, listed and
// reachable.
func TestServiceScriptsOff(t *testing.T) {
	f := open(t, map[string]any{"s.json": serviceSpec(t, "on"), "p.json": pluginSpec(t, "gone_run")})
	serve(t, f, true)
	started(t, f, "on")
	c := dial(t, f)
	if got := toolNames(c.tools()); got != builtins+","+mcpServiceTools {
		t.Fatalf("tools = %s", got)
	}
	if r := c.call("svc_echo", map[string]any{}); r.IsError {
		t.Errorf("service call with scripts off: %s", r.text())
	}
}

// CAP-RGB-014: restart backoff 1 s doubling to 30 s, 1 s again after a run
// of 30 s.
func TestServiceBackoff(t *testing.T) {
	var got []time.Duration
	var w time.Duration
	for i := 0; i < 8; i++ {
		w = extensions.NextBackoff(w, 100*time.Millisecond)
		got = append(got, w)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatalf("backoff %v", got)
		}
	}
	if extensions.NextBackoff(30*time.Second, 30*time.Second) != time.Second {
		t.Error("a 30 s run must reset the backoff")
	}
	if extensions.NextBackoff(8*time.Second, 29*time.Second) != 16*time.Second {
		t.Error("a 29 s run keeps doubling")
	}
}

// CAP-DMN-039: a service that dies is restarted after 1 s and binds again
// even though it left its socket file; the services directory is 0700.
func TestServiceRestart(t *testing.T) {
	f := open(t, map[string]any{"s.json": serviceSpec(t, "rst")})
	pid := started(t, f, "rst")
	fi, err := os.Stat(f.x.ServicesDir())
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("services dir: %v %v", fi, err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	died := time.Now()
	if !exists(f.x.Socket("rst")) {
		t.Fatal("the killed service should leave its socket file")
	}
	var next int
	waitFor(t, 3*time.Second, "a restart", func() bool {
		next = f.x.ServicePID("rst")
		return next > 0 && next != pid
	})
	if took := time.Since(died); took < 900*time.Millisecond {
		t.Errorf("restarted after %s, before the 1 s backoff", took)
	}
	reply, err := f.x.CallService(context.Background(), "svc_echo", "a", nil)
	if err != nil || !strings.Contains(string(reply), `"pid":`+strconv.Itoa(next)) {
		t.Fatalf("after restart: %s %v", reply, err)
	}
}

// startStale leaves a copy listening on the service's socket, as a killed
// daemon would; leader puts it in a process group of its own.
func startStale(t *testing.T, base, service string, leader, ignoreTerm bool) (*exec.Cmd, chan struct{}) {
	t.Helper()
	dir := filepath.Join(base, "d", "services")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, service+".sock")
	args := []string{"stale", sock}
	if ignoreTerm {
		args = append(args, "ignore")
	}
	cmd := exec.Command(self(t), args...)
	cmd.Env = testEnv(base)
	if leader {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	gone := make(chan struct{})
	go func() { _ = cmd.Wait(); close(gone) }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	waitFor(t, 3*time.Second, "the stale copy to listen", func() bool { return exists(sock) })
	return cmd, gone
}

// openIn is open with a fixed base directory.
func openIn(t *testing.T, base string, specs map[string]any) *fixture {
	t.Helper()
	f := &fixture{dir: filepath.Join(base, "d"), state: filepath.Join(base, "s"), logs: newLogs()}
	_ = os.MkdirAll(f.state, 0o700)
	for name, v := range specs {
		writeSpec(t, filepath.Join(base, "tools.d"), name, v)
	}
	x, err := extensions.Open(context.Background(), extensions.Options{ConfigPath: filepath.Join(base, "config.toml"), DaemonDir: f.dir, Env: testEnv(f.state), Log: f.logs.f})
	if err != nil {
		t.Fatal(err)
	}
	f.x = x
	t.Cleanup(x.Close)
	return f
}

// CAP-DMN-039: a copy left by a killed daemon, leading a process group or
// not, is stopped before the service starts again.
func TestServiceStaleCopy(t *testing.T) {
	for _, leader := range []bool{true, false} {
		t.Run("leader="+strconv.FormatBool(leader), func(t *testing.T) {
			base := shortDir(t)
			stale, gone := startStale(t, base, "dup", leader, false)
			f := openIn(t, base, map[string]any{"s.json": serviceSpec(t, "dup")})
			select {
			case <-gone:
			case <-time.After(2 * time.Second):
				t.Fatalf("the stale copy is still running: %q", f.logs.all())
			}
			pid := started(t, f, "dup")
			if pid == stale.Process.Pid {
				t.Fatal("the stale copy answers")
			}
		})
	}
}

// CAP-RGA-090: a stale copy that ignores SIGTERM is killed 3 s later, so
// restarts never add copies.
func TestStaleServiceStopped(t *testing.T) {
	base := shortDir(t)
	_, gone := startStale(t, base, "stub", true, true)
	start := time.Now()
	f := openIn(t, base, map[string]any{"s.json": serviceSpec(t, "stub")})
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the stale copy survived")
	}
	if took := time.Since(start); took < 2900*time.Millisecond {
		t.Errorf("killed after %s, before the 3 s grace", took)
	}
	started(t, f, "stub")
}

// CAP-DMN-039: at daemon exit a service gets SIGTERM, then SIGKILL 3 s
// later, with the processes it started.
func TestServiceStopsWithDaemon(t *testing.T) {
	kidOf := func(f *fixture, pid int) int {
		var kid int
		waitFor(t, 3*time.Second, "the service's child", func() bool {
			data, err := os.ReadFile(filepath.Join(f.state, "kid-"+strconv.Itoa(pid)))
			kid, _ = strconv.Atoi(string(data))
			return err == nil && kid > 0
		})
		return kid
	}
	f := open(t, map[string]any{"s.json": serviceSpec(t, "bye")})
	pid := started(t, f, "bye")
	kid := kidOf(f, pid)
	start := time.Now()
	f.x.Close()
	if took := time.Since(start); took > time.Second {
		t.Errorf("a service that honours SIGTERM took %s", took)
	}
	waitFor(t, 2*time.Second, "the service and its child to end", func() bool { return !alive(pid) && !alive(kid) })
	if exists(f.x.Socket("bye")) {
		t.Error("the socket was left behind")
	}

	g := open(t, map[string]any{"s.json": serviceSpec(t, "stubborn", "ignore")})
	pid = started(t, g, "stubborn")
	kid = kidOf(g, pid)
	start = time.Now()
	g.x.Close()
	if took := time.Since(start); took < 2900*time.Millisecond || took > 4*time.Second {
		t.Errorf("a service that ignores SIGTERM ended after %s, want 3 s", took)
	}
	waitFor(t, 2*time.Second, "the stubborn service and its child to end", func() bool { return !alive(pid) && !alive(kid) })
}
