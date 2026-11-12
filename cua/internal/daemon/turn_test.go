package daemon

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/engine"
	"fennec/cua/internal/mcpclient"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
)

type logSink struct {
	mu  sync.Mutex
	out *[]string
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.out = append(*s.out, string(p))
	return len(p), nil
}

func newTestLogger(out *[]string) *log.Logger { return log.New(&logSink{out: out}, "", 0) }

// actLog is a fake act tool: it takes the turn, then sends each key of
// its steps one by one (recorded in order), pausing between them.
type actLog struct {
	mu      sync.Mutex
	sent    []string
	started chan string // the first key of each act, once it holds the turn
	hold    chan struct{}
	pause   time.Duration
	holds   int // successful Call.Hold registrations
	runIDs  []string
	turn    *sched.Turn
}

func (a *actLog) tool(ctx context.Context, call *engine.Call, args json.RawMessage) (any, *protocol.Error) {
	var req protocol.ActRequest
	if perr := engine.Decode(args, &req); perr != nil {
		return nil, perr
	}
	release, err := call.Turn(ctx)
	if err != nil {
		return nil, engine.TurnError(err)
	}
	defer release()
	a.mu.Lock()
	if a.turn != nil {
		a.runIDs = append(a.runIDs, a.turn.Holder())
	}
	a.mu.Unlock()
	if call.Hold(func() { a.mu.Lock(); a.holds--; a.mu.Unlock() }) {
		a.mu.Lock()
		a.holds++
		a.mu.Unlock()
	}
	if a.started != nil && len(req.Steps) > 0 {
		a.started <- req.Steps[0].Key
	}
	if a.hold != nil {
		select {
		case <-a.hold:
		case <-ctx.Done():
			return nil, nil
		}
	}
	for _, s := range req.Steps {
		if ctx.Err() != nil {
			return nil, nil
		}
		a.mu.Lock()
		a.sent = append(a.sent, s.Key)
		a.mu.Unlock()
		time.Sleep(a.pause)
	}
	return map[string]any{"ok": true, "sent": len(req.Steps)}, nil
}

func (a *actLog) log() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.sent, "")
}

func actEngine(a *actLog, busy time.Duration) *engine.Engine {
	eng := newEngine(nil)
	eng.Turn.Busy = busy
	a.turn = eng.Turn
	eng.Impl = map[string]engine.Tool{protocol.ToolAct: a.tool}
	eng.Impl[protocol.ToolRun] = eng.RunTool(nil)
	return eng
}

func keys(k string, n int) map[string]any {
	steps := make([]any, n)
	for i := range steps {
		steps[i] = map[string]any{"key": k}
	}
	return map[string]any{"steps": steps}
}

func errCode(r mcpclient.ToolResult) string {
	var out struct {
		Error *protocol.Error `json:"error"`
	}
	_ = json.Unmarshal([]byte(r.Text()), &out)
	if out.Error == nil {
		return ""
	}
	return string(out.Error.Code)
}

// runScript is a script whose main acts keys of tag n times.
func runScript(tag string, n int, extra string) map[string]any {
	return map[string]any{"code": `export async function main() {
		for (let i = 0; i < ` + itoa(n) + `; i++) { await cua.act({steps: [{key: "` + tag + `"}]}) }
		` + extra + `
		return "done"
	}`}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestTurnBusyBound(t *testing.T) {
	if sched.DefaultBusy != 15*time.Second || (&sched.Turn{}).Bound() != 15*time.Second {
		t.Fatalf("default busy bound %v", sched.DefaultBusy)
	}
	a := &actLog{pause: 40 * time.Millisecond}
	eng := actEngine(a, 300*time.Millisecond)
	p, _, _ := startDaemon(t, eng)
	runner, other := dialMCP(t, p), dialMCP(t, p)
	ctx := context.Background()
	got := make(chan mcpclient.ToolResult, 1)
	go func() {
		r, _ := runner.Tool(ctx, "run", runScript("r", 12, ""))
		got <- r
	}()
	waitFor(t, func() bool { return strings.Contains(a.log(), "r") })
	start := time.Now()
	r, err := other.Tool(ctx, "act", keys("x", 1))
	took := time.Since(start)
	if err != nil || errCode(r) != "busy" || !strings.Contains(r.Text(), "nothing was sent") {
		t.Fatalf("an act while a script holds the turn: %s %v", r.Text(), err)
	}
	if took < 300*time.Millisecond {
		t.Errorf("busy after %v, before the bound", took)
	}
	run := <-got
	if run.IsError || a.log() != strings.Repeat("r", 12) {
		t.Errorf("the script was disturbed: %s, sent %q", run.Text(), a.log())
	}
	// Reads take no turn: status and a read tool pass while a run acts.
	if r, _ := other.Tool(ctx, "status", nil); r.IsError {
		t.Errorf("status during a run: %s", r.Text())
	}
}

func TestRunsNeverInterleave(t *testing.T) {
	a := &actLog{pause: 15 * time.Millisecond}
	eng := actEngine(a, 10*time.Second)
	p, _, _ := startDaemon(t, eng)
	one, two := dialMCP(t, p), dialMCP(t, p)
	var wg sync.WaitGroup
	for _, c := range []struct {
		cl  *mcpclient.Client
		tag string
	}{{one, "a"}, {two, "b"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := c.cl.Tool(context.Background(), "run", runScript(c.tag, 6, ""))
			if err != nil || r.IsError {
				t.Errorf("run %s: %s %v", c.tag, r.Text(), err)
			}
		}()
	}
	wg.Wait()
	if got := a.log(); got != "aaaaaabbbbbb" && got != "bbbbbbaaaaaa" {
		t.Errorf("two runs interleaved: %q", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunIDsUnique(t *testing.T) {
	var a, b sched.RunIDs
	ids := map[string]bool{}
	for i := 0; i < 100; i++ {
		for _, r := range []*sched.RunIDs{&a, &b} {
			id := r.Next()
			if ids[id] {
				t.Fatalf("id %s handed out twice", id)
			}
			ids[id] = true
		}
	}
	// Two daemons (one before a restart, one after) name their first runs
	// apart; a run's steps pass as the run, not as its session.
	var holders []string
	for i := 0; i < 2; i++ {
		act := &actLog{}
		eng := actEngine(act, time.Second)
		p, _, _ := startDaemon(t, eng)
		c := dialMCP(t, p)
		for j := 0; j < 2; j++ {
			if r, err := c.Tool(context.Background(), "run", runScript("k", 1, "")); err != nil || r.IsError {
				t.Fatalf("run: %s %v", r.Text(), err)
			}
		}
		if len(act.runIDs) != 2 || act.runIDs[0] == act.runIDs[1] {
			t.Fatalf("runs of one daemon: %q", act.runIDs)
		}
		holders = append(holders, act.runIDs...)
		_ = c.Close()
	}
	seen := map[string]bool{}
	for _, h := range holders {
		if !strings.HasPrefix(h, "run-") || seen[h] || strings.HasPrefix(h, "mcp-") {
			t.Errorf("run owner %q among %q", h, holders)
		}
		seen[h] = true
	}
	if strings.Split(holders[0], "-")[1] == strings.Split(holders[2], "-")[1] {
		t.Errorf("a restarted daemon reused the nonce: %q", holders)
	}
}

func TestRunReleasesHolds(t *testing.T) {
	a := &actLog{}
	eng := actEngine(a, time.Second)
	p, _, _ := startDaemon(t, eng)
	c := dialMCP(t, p)
	ctx := context.Background()
	if r, err := c.Tool(ctx, "run", runScript("k", 3, "")); err != nil || r.IsError {
		t.Fatalf("run: %s %v", r.Text(), err)
	}
	a.mu.Lock()
	if a.holds != 0 {
		t.Errorf("%d holds left after a run ended", a.holds)
	}
	a.mu.Unlock()
	// A failing run lets go too.
	if r, _ := c.Tool(ctx, "run", runScript("k", 2, `throw new Error("boom")`)); !r.IsError {
		t.Errorf("failing run answered %s", r.Text())
	}
	a.mu.Lock()
	if a.holds != 0 {
		t.Errorf("%d holds left after a failed run", a.holds)
	}
	a.mu.Unlock()
	// Outside a run nothing is held for the caller.
	if r, _ := c.Tool(ctx, "act", keys("x", 1)); r.IsError {
		t.Fatal(r.Text())
	}
	a.mu.Lock()
	if a.holds != 0 {
		t.Errorf("an act outside a run registered %d holds", a.holds)
	}
	a.mu.Unlock()

	// A dropped run lets go of its holds.
	dropped, err := mcpclient.Dial(ctx, p.MCP)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = dropped.Tool(ctx, "run", runScript("d", 1, "await sleep(60000)")) }()
	waitFor(t, func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.holds == 1 })
	_ = dropped.Close()
	waitFor(t, func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.holds == 0 })
}

func TestActingState(t *testing.T) {
	a := &actLog{hold: make(chan struct{}), started: make(chan string, 4)}
	eng := actEngine(a, time.Second)
	var mu sync.Mutex
	var states []bool
	eng.Turn.OnActing(func(b bool) { mu.Lock(); states = append(states, b); mu.Unlock() })
	p, _, _ := startDaemon(t, eng)
	c := dialMCP(t, p)
	ctx := context.Background()
	if eng.Turn.Acting() {
		t.Fatal("acting before any call")
	}
	// An action ended by a stop.
	go func() { _, _ = c.Tool(ctx, "act", keys("x", 1)) }()
	<-a.started
	if !eng.Turn.Acting() {
		t.Error("not acting during an action")
	}
	if _, err := Control(ctx, p, "stop"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !eng.Turn.Acting() })
	if _, err := Control(ctx, p, "resume"); err != nil {
		t.Fatal(err)
	}
	// A run that is dropped.
	close(a.hold)
	dropped, err := mcpclient.Dial(ctx, p.MCP)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = dropped.Tool(ctx, "run", runScript("d", 1, "await sleep(60000)")) }()
	<-a.started
	waitFor(t, func() bool { return eng.Turn.Acting() })
	_ = dropped.Close()
	waitFor(t, func() bool { return !eng.Turn.Acting() })
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(states) == 4
	})
	mu.Lock()
	defer mu.Unlock()
	if !states[0] || states[1] || !states[2] || states[3] {
		t.Errorf("acting states %v", states)
	}
}

// dropRun starts a run on a connection of its own, waits until it holds
// the turn, then drops the connection; it answers when.
func dropRun(t *testing.T, p Paths, a *actLog, code map[string]any) time.Time {
	t.Helper()
	c, err := mcpclient.Dial(context.Background(), p.MCP)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = c.Tool(context.Background(), "run", code) }()
	<-a.started
	time.Sleep(20 * time.Millisecond)
	dropped := time.Now()
	_ = c.Close()
	return dropped
}

func TestDisconnectCancels(t *testing.T) {
	a := &actLog{started: make(chan string, 4)}
	eng := actEngine(a, 10*time.Second)
	p, _, _ := startDaemon(t, eng)
	// A script spinning in a busy loop is interrupted once its client
	// goes, and the next run starts within 5 s.
	dropped := dropRun(t, p, a, runScript("s", 1, "while (true) {}"))
	waitFor(t, func() bool { return eng.Turn.Holder() == "" })
	if took := time.Since(dropped); took > 3*time.Second {
		t.Errorf("interrupted after %v", took)
	}
	c := dialMCP(t, p)
	start := time.Now()
	r, err := c.Tool(context.Background(), "run", runScript("n", 1, ""))
	if err != nil || r.IsError {
		t.Fatalf("next run: %s %v", r.Text(), err)
	}
	if time.Since(dropped) > 5*time.Second || time.Since(start) > 2*time.Second {
		t.Errorf("the next run waited %v", time.Since(start))
	}
	// Later requests of a session whose earlier call was cancelled go on.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-a.started; cancel() }()
	_, _ = c.Tool(ctx, "run", runScript("c", 1, "await sleep(60000)"))
	if r, err := c.Tool(context.Background(), "act", keys("z", 1)); err != nil || r.IsError {
		t.Errorf("after a cancel: %s %v", r.Text(), err)
	}
}

func TestDisconnectCancelsCall(t *testing.T) {
	a := &actLog{started: make(chan string, 4)}
	eng := actEngine(a, 30*time.Second)
	p, _, _ := startDaemon(t, eng)
	// A run waiting out a long sleep holds the turn; dropping its client
	// ends it as a stop would and frees the turn at once, not at the run
	// limit.
	dropped := dropRun(t, p, a, runScript("s", 1, "await sleep(90000)"))
	waitFor(t, func() bool { return eng.Turn.Holder() == "" })
	if took := time.Since(dropped); took > time.Second {
		t.Errorf("turn freed after %v", took)
	}
	if eng.Stopper.Stopped() {
		t.Error("a dropped client stopped fennec-cua for everyone")
	}
	// A connection that stays open never cancels its call.
	c := dialMCP(t, p)
	a.pause = 300 * time.Millisecond
	r, err := c.Tool(context.Background(), "act", keys("o", 2))
	if err != nil || r.IsError || !strings.Contains(r.Text(), `"sent":2`) {
		t.Errorf("an open connection's call: %s %v", r.Text(), err)
	}
}

func TestCancelledWaitFreesDesk(t *testing.T) {
	waiting := make(chan struct{}, 1)
	eng := newEngine(map[string]engine.Tool{
		protocol.ToolWait: func(ctx context.Context, call *engine.Call, _ json.RawMessage) (any, *protocol.Error) {
			release, err := call.Turn(ctx)
			if err != nil {
				return nil, engine.TurnError(err)
			}
			defer release()
			waiting <- struct{}{}
			<-ctx.Done()
			return nil, nil
		},
	})
	eng.Turn.Busy = 5 * time.Second
	p, _, _ := startDaemon(t, eng)
	c := dialMCP(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-waiting; cancel() }()
	_, err := c.Tool(ctx, "wait", map[string]any{"timeout_s": 60})
	if err == nil {
		t.Fatal("the cancelled wait answered")
	}
	cancelled := time.Now()
	waitFor(t, func() bool { return eng.Turn.Holder() == "" })
	if took := time.Since(cancelled); took > 500*time.Millisecond {
		t.Errorf("desk freed %v after the cancel", took)
	}
	release, err := eng.Turn.Acquire(context.Background(), "next")
	if err != nil {
		t.Fatalf("the next call: %v", err)
	}
	release()
}

func TestSingleTurn(t *testing.T) {
	// act, vision acts (pixel steps of act), launches and scripts all
	// take the engine's one turn: while a script holds it, every other
	// input path of another session waits and answers busy.
	a := &actLog{started: make(chan string, 4), hold: make(chan struct{})}
	eng := actEngine(a, 200*time.Millisecond)
	p, _, _ := startDaemon(t, eng)
	runner, other := dialMCP(t, p), dialMCP(t, p)
	ctx := context.Background()
	got := make(chan mcpclient.ToolResult, 1)
	go func() {
		r, _ := runner.Tool(ctx, "run", runScript("r", 1, ""))
		got <- r
	}()
	<-a.started
	for _, args := range []map[string]any{
		keys("k", 1),
		{"steps": []any{map[string]any{"launch": "TextEdit"}}},
		{"steps": []any{map[string]any{"click": map[string]any{"point": map[string]any{"x": 5, "y": 5, "space": "image"}}}}},
	} {
		if r, _ := other.Tool(ctx, "act", args); errCode(r) != "busy" {
			t.Errorf("act %v during a run: %s", args, r.Text())
		}
	}
	if r, _ := other.Tool(ctx, "run", runScript("o", 1, "")); errCode(r) != "busy" {
		t.Errorf("a second run: %s", r.Text())
	}
	close(a.hold)
	if r := <-got; r.IsError {
		t.Errorf("the run: %s", r.Text())
	}
	if a.log() != "r" {
		t.Errorf("sent %q", a.log())
	}
}

func TestBusyAfter15s(t *testing.T) {
	if got := (&sched.Turn{}).Bound(); got != 15*time.Second {
		t.Errorf("default bound %v", got)
	}
	if got := (&sched.Turn{Busy: time.Second}).Bound(); got != time.Second {
		t.Errorf("configured bound %v", got)
	}
	// The engine's turn waits out its bound, then answers busy.
	a := &actLog{started: make(chan string, 2), hold: make(chan struct{})}
	eng := actEngine(a, 250*time.Millisecond)
	p, _, _ := startDaemon(t, eng)
	one, two := dialMCP(t, p), dialMCP(t, p)
	go func() { _, _ = one.Tool(context.Background(), "act", keys("a", 1)) }()
	<-a.started
	start := time.Now()
	r, _ := two.Tool(context.Background(), "act", keys("b", 1))
	if errCode(r) != "busy" || time.Since(start) < 250*time.Millisecond {
		t.Errorf("after %v: %s", time.Since(start), r.Text())
	}
	close(a.hold)
}

func TestStopEndsWaiter(t *testing.T) {
	a := &actLog{started: make(chan string, 2), hold: make(chan struct{})}
	eng := actEngine(a, 10*time.Second)
	p, _, _ := startDaemon(t, eng)
	one, two := dialMCP(t, p), dialMCP(t, p)
	go func() { _, _ = one.Tool(context.Background(), "act", keys("a", 1)) }()
	<-a.started
	got := make(chan mcpclient.ToolResult, 1)
	go func() {
		r, _ := two.Tool(context.Background(), "act", keys("b", 1))
		got <- r
	}()
	time.Sleep(50 * time.Millisecond)
	stopped := time.Now()
	if _, err := Control(context.Background(), p, "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if errCode(r) != "stopped" {
			t.Errorf("the waiter answered %s", r.Text())
		}
		if time.Since(stopped) > time.Second {
			t.Errorf("waiter ended %v after the stop", time.Since(stopped))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stop left the waiter waiting")
	}
	if a.log() != "" {
		t.Errorf("sent %q", a.log())
	}
}
