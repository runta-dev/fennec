package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fennec/cua/internal/engine"
	"fennec/cua/internal/mcpclient"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/record"
	"fennec/cua/internal/view"
)

func session(agent string) *mcpserver.Session { return &mcpserver.Session{AgentID: agent} }

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// asOtherUser makes the daemon take its own uid for another one, so the
// test's own connections arrive as a shared-group peer would.
func asOtherUser(t *testing.T) {
	t.Helper()
	real := ownUID()
	ownUID = func() uint32 { return real + 1 }
	t.Cleanup(func() { ownUID = func() uint32 { return real } })
}

// quietDesk is a recorder desk with nothing on it.
type quietDesk struct{}

func (quietDesk) Windows(context.Context) ([]view.RawWindow, error)    { return nil, nil }
func (quietDesk) At(context.Context, int, int) (*record.Hit, error)    { return nil, nil }
func (quietDesk) Shot(context.Context, view.RawWindow) ([]byte, error) { return nil, nil }
func (quietDesk) HoldWeb(context.Context, int) error                   { return nil }
func (quietDesk) ReleaseWeb(context.Context)                           {}
func (quietDesk) Bundle(int) string                                    { return "" }

// recorderRig counts the event taps a recorder opens.
type recorderRig struct {
	taps atomic.Int32
	rec  *record.Recorder
}

func newRecorder(t *testing.T, stopped func() bool) *recorderRig {
	r := &recorderRig{}
	r.rec = &record.Recorder{Dir: filepath.Join(t.TempDir(), "rec"), Desk: quietDesk{}, Stopped: stopped, Settle: time.Millisecond,
		Listen: func(func(record.Input)) (func(), error) { r.taps.Add(1); return func() {}, nil }}
	return r
}

// confirmer answers the record question; it reports each question and
// whether its dialog was closed by the call ending.
type confirmer struct {
	answer  chan bool
	asked   chan string
	mu      sync.Mutex
	closedN int
}

func (c *confirmer) Confirm(ctx context.Context, q string) (bool, error) {
	c.asked <- q
	select {
	case ok := <-c.answer:
		return ok, nil
	case <-ctx.Done():
		c.mu.Lock()
		c.closedN++
		c.mu.Unlock()
		return false, ctx.Err()
	}
}

func flowEngine(t *testing.T, confirm string, c policy.Confirmer) (*served, *recorderRig) {
	t.Helper()
	pol, err := policy.New(policy.Config{Confirm: confirm}, c)
	if err != nil {
		t.Fatal(err)
	}
	eng := newEngine(nil)
	rr := newRecorder(t, eng.Stopper.Stopped)
	eng.Impl = map[string]engine.Tool{
		protocol.ToolFlow: engine.FlowTool(&engine.Flows{Recorder: rr.rec, Gate: pol.Gate}),
	}
	t.Cleanup(func() { _, _ = rr.rec.Stop(context.Background()) })
	return eng, rr
}

func TestOwnerOnlyRefusedForOtherUser(t *testing.T) {
	if err := OwnerOnly(502, 501); err == nil || !strings.Contains(err.Error(), "only for the daemon's own user (uid 501)") || !strings.Contains(err.Error(), "uid 502") {
		t.Errorf("another user: %v", err)
	}
	d := &Daemon{Stopper: newEngine(nil).Stopper}
	for _, op := range []string{"stop", "resume", "status"} {
		rep := d.control(context.Background(), 502, 501, ControlRequest{Op: op})
		if rep.OK || !strings.Contains(rep.Error, op+": stop, resume and recording are answered only for the daemon's own user") {
			t.Errorf("%s from another user: %+v", op, rep)
		}
	}
	if d.Stopper.Stopped() {
		t.Error("another user stopped the daemon")
	}

	// Over the sockets: a group peer gets an error naming the restriction
	// on the control socket while its tool calls are answered.
	name, _ := ownGroup(t)
	asOtherUser(t)
	eng, rr := flowEngine(t, "off", nil)
	p := PathsIn(filepath.Join(realTempDir(t), "d"))
	runDaemon(t, &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper, SocketGroup: name})
	_, err := Control(context.Background(), p, "stop")
	if err == nil || !strings.Contains(err.Error(), "only for the daemon's own user") {
		t.Errorf("control from a group peer: %v", err)
	}
	if eng.Stopper.Stopped() {
		t.Error("a group peer stopped the daemon")
	}
	c := dialMCP(t, p)
	if r, err := c.Tool(context.Background(), "status", nil); err != nil || r.IsError {
		t.Errorf("a group peer's tool call: %v %v", r, err)
	}
	if r, _ := c.Tool(context.Background(), "flow", map[string]any{"op": "record_start"}); errCode(r) != "not_allowed" || !strings.Contains(r.Text(), "own user") {
		t.Errorf("a group peer's recording: %s", r.Text())
	}
	if rr.taps.Load() != 0 {
		t.Error("a group peer started a recording")
	}

	// Without a shared group, another user's agent connection is refused.
	p = PathsIn(filepath.Join(realTempDir(t), "d"))
	runDaemon(t, &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper})
	if cl, err := mcpclient.Dial(context.Background(), p.MCP); err == nil {
		defer cl.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := cl.Tool(ctx, "status", nil); err == nil {
			t.Error("a private socket answered another user")
		}
	}
}

func TestOwnerOnlyAnsweredForOwnUser(t *testing.T) {
	if err := OwnerOnly(501, 501); err != nil {
		t.Fatal(err)
	}
	name, _ := ownGroup(t)
	eng, rr := flowEngine(t, "off", nil)
	p := PathsIn(filepath.Join(realTempDir(t), "d"))
	runDaemon(t, &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper, SocketGroup: name})
	ctx := context.Background()
	if rep, err := Control(ctx, p, "status"); err != nil || !rep.OK {
		t.Errorf("status: %+v %v", rep, err)
	}
	if _, err := Control(ctx, p, "stop"); err != nil || !eng.Stopper.Stopped() {
		t.Errorf("stop: %v", err)
	}
	if _, err := Control(ctx, p, "resume"); err != nil || eng.Stopper.Stopped() {
		t.Errorf("resume: %v", err)
	}
	c := dialMCP(t, p)
	if r, _ := c.Tool(ctx, "flow", map[string]any{"op": "record_start"}); r.IsError || rr.taps.Load() != 1 {
		t.Errorf("the owner's recording: %s", r.Text())
	}
}

func TestStopOnlyFromOwnUser(t *testing.T) {
	d := &Daemon{Stopper: newEngine(nil).Stopper}
	if rep := d.control(context.Background(), 1000, 501, ControlRequest{Op: "stop"}); rep.OK || d.Stopper.Stopped() {
		t.Errorf("stop from another user: %+v", rep)
	}
	if rep := d.control(context.Background(), 501, 501, ControlRequest{Op: "stop"}); !rep.OK || !d.Stopper.Stopped() {
		t.Errorf("stop from the owner: %+v", rep)
	}
	if rep := d.control(context.Background(), 1000, 501, ControlRequest{Op: "resume"}); rep.OK || !d.Stopper.Stopped() {
		t.Errorf("resume from another user: %+v", rep)
	}
	// Further operations answer the owner only, with their arguments.
	d.Ops = map[string]func(context.Context, json.RawMessage) (any, error){
		"browser.import": func(_ context.Context, args json.RawMessage) (any, error) { return string(args), nil },
	}
	if rep := d.control(context.Background(), 1000, 501, ControlRequest{Op: "browser.import"}); rep.OK {
		t.Errorf("an operation from another user: %+v", rep)
	}
	if rep := d.control(context.Background(), 501, 501, ControlRequest{Op: "browser.import", Args: json.RawMessage(`{"profile":"Default"}`)}); !rep.OK || rep.Status != `{"profile":"Default"}` {
		t.Errorf("an operation from the owner: %+v", rep)
	}
	if rep := d.control(context.Background(), 501, 501, ControlRequest{Op: "browser.nope"}); rep.OK {
		t.Errorf("an unknown operation: %+v", rep)
	}
	// No MCP tool reaches stop or resume, whatever its name.
	eng := newEngine(nil)
	p, _, _ := startDaemon(t, eng)
	c := dialMCP(t, p)
	for _, name := range []string{"stop", "resume", "../stop", "safety_stop", "control/stop"} {
		_, _ = c.Tool(context.Background(), name, nil)
	}
	if eng.Stopper.Stopped() {
		t.Error("an MCP call stopped the daemon")
	}
}

func TestRecordOwnUserOnly(t *testing.T) {
	eng, rr := flowEngine(t, "off", nil)
	other := engine.WithPeer(context.Background(), false)
	for _, op := range []string{"record_start", "record_note", "record_stop"} {
		args, _ := json.Marshal(map[string]any{"op": op, "text": "x"})
		r := eng.Call(other, session("g"), "flow", args)
		if !r.IsError || !strings.Contains(r.Text, `"not_allowed"`) || !strings.Contains(r.Text, "own user") {
			t.Errorf("%s from a group peer: %s", op, r.Text)
		}
	}
	if rr.taps.Load() != 0 {
		t.Error("a group peer started a recording")
	}
	own := engine.WithPeer(context.Background(), true)
	if r := eng.Call(own, session("o"), "flow", json.RawMessage(`{"op":"record_start"}`)); r.IsError || rr.taps.Load() != 1 {
		t.Errorf("the owner: %s", r.Text)
	}
	if r := eng.Call(own, session("o"), "flow", json.RawMessage(`{"op":"record_note","text":"here"}`)); r.IsError {
		t.Errorf("the owner's note: %s", r.Text)
	}
}

func TestRecordStartConfirm(t *testing.T) {
	c := &confirmer{answer: make(chan bool, 1), asked: make(chan string, 4)}
	eng, rr := flowEngine(t, "coordinate", c)
	eng.Turn.Busy = 150 * time.Millisecond
	ctx := context.Background()
	start := json.RawMessage(`{"op":"record_start"}`)

	// Busy: no question.
	release, _ := eng.Turn.Acquire(ctx, "other")
	if r := eng.Call(ctx, session("a"), "flow", start); !strings.Contains(r.Text, `"busy"`) {
		t.Errorf("busy desk: %s", r.Text)
	}
	select {
	case q := <-c.asked:
		t.Errorf("asked %q without the turn", q)
	default:
	}
	release()

	// A no starts nothing; the question is asked while holding the turn.
	holder := make(chan string, 1)
	go func() { <-c.asked; holder <- eng.Turn.Holder(); c.answer <- false }()
	if r := eng.Call(ctx, session("a"), "flow", start); !strings.Contains(r.Text, `"declined"`) {
		t.Errorf("declined: %s", r.Text)
	}
	if h := <-holder; h != "a" {
		t.Errorf("asked while %q held the turn", h)
	}
	if rr.taps.Load() != 0 {
		t.Error("a declined recording started")
	}

	// A yes starts it, with the product's question.
	c.answer <- true
	q := make(chan string, 1)
	go func() { q <- <-c.asked }()
	if r := eng.Call(ctx, session("a"), "flow", start); r.IsError || rr.taps.Load() != 1 {
		t.Errorf("accepted: %s", r.Text)
	}
	if got := <-q; got != "Allow fennec-cua to record your input?" {
		t.Errorf("question %q", got)
	}
	_, _ = rr.rec.Stop(ctx)

	// confirm off asks nothing.
	eng2, rr2 := flowEngine(t, "off", c)
	if r := eng2.Call(ctx, session("a"), "flow", start); r.IsError || rr2.taps.Load() != 1 {
		t.Errorf("confirm off: %s", r.Text)
	}
	select {
	case q := <-c.asked:
		t.Errorf("asked %q with confirm off", q)
	default:
	}
}

func TestRecordStartStop(t *testing.T) {
	c := &confirmer{answer: make(chan bool, 1), asked: make(chan string, 4)}
	eng, rr := flowEngine(t, "every", c)
	eng.Turn.Busy = 10 * time.Second
	ctx := context.Background()
	start := json.RawMessage(`{"op":"record_start"}`)

	// Refused while stopped.
	eng.Stopper.Stop()
	if r := eng.Call(ctx, session("a"), "flow", start); !strings.Contains(r.Text, `"stopped"`) {
		t.Errorf("while stopped: %s", r.Text)
	}
	rr.rec.Stopped = eng.Stopper.Stopped
	if _, err := rr.rec.Start(nil); err == nil {
		t.Error("the menu started a recording while stopped")
	}
	eng.Stopper.Resume()

	// A stop while it waits for the turn.
	release, _ := eng.Turn.Acquire(ctx, "other")
	got := make(chan string, 1)
	go func() { got <- eng.Call(ctx, session("a"), "flow", start).Text }()
	time.Sleep(50 * time.Millisecond)
	eng.Stopper.Stop()
	if text := <-got; !strings.Contains(text, `"stopped"`) {
		t.Errorf("stopped while waiting for the turn: %s", text)
	}
	release()
	eng.Stopper.Resume()

	// A stop while its question is open closes the question.
	go func() { got <- eng.Call(ctx, session("a"), "flow", start).Text }()
	<-c.asked
	eng.Stopper.Stop()
	if text := <-got; !strings.Contains(text, `"stopped"`) {
		t.Errorf("stopped while asking: %s", text)
	}
	c.mu.Lock()
	if c.closedN != 1 {
		t.Errorf("question closed %d times", c.closedN)
	}
	c.mu.Unlock()
	if rr.taps.Load() != 0 || eng.Turn.Holder() != "" {
		t.Errorf("after the stops: %d taps, holder %q", rr.taps.Load(), eng.Turn.Holder())
	}

	// Input works again after resume.
	eng.Stopper.Resume()
	c.answer <- true
	go func() { <-c.asked }()
	if r := eng.Call(ctx, session("a"), "flow", start); r.IsError || rr.taps.Load() != 1 {
		t.Errorf("after resume: %s", r.Text)
	}
}

func TestRecordStartArgs(t *testing.T) {
	eng, rr := flowEngine(t, "off", nil)
	for _, args := range []string{`{"op":"record_start","apps":"TextEdit"}`, `{"op":"record_start","apps":[1,2]}`, `{"op":"record_start","apps":{"a":1}}`} {
		r := eng.Call(context.Background(), session("a"), "flow", json.RawMessage(args))
		if !strings.Contains(r.Text, `"invalid_argument"`) {
			t.Errorf("%s: %s", args, r.Text)
		}
	}
	if rr.taps.Load() != 0 {
		t.Error("bad arguments started a recording")
	}
	if r := eng.Call(context.Background(), session("a"), "flow", json.RawMessage(`{"op":"record_start","apps":["TextEdit"]}`)); r.IsError || rr.taps.Load() != 1 {
		t.Errorf("a list of apps: %s", r.Text)
	}
}

func TestStatusInputMode(t *testing.T) {
	for _, c := range []struct{ configured, want string }{{"", "foreground"}, {"background", "background"}, {"auto", "auto"}} {
		eng := newEngine(nil)
		eng.Input = c.configured
		p, _, _ := startDaemon(t, eng)
		cl := dialMCP(t, p)
		r, err := cl.Tool(context.Background(), "status", nil)
		if err != nil {
			t.Fatal(err)
		}
		var st protocol.StatusResult
		if err := json.Unmarshal([]byte(r.Text()), &st); err != nil || st.Input != c.want {
			t.Errorf("configured %q: MCP status input %q (%v)", c.configured, st.Input, err)
		}
		rep, err := Control(context.Background(), p, "status")
		if err != nil || !strings.Contains(mustJSON(rep.Status), `"input":"`+c.want+`"`) {
			t.Errorf("configured %q: control status %s %v", c.configured, mustJSON(rep.Status), err)
		}
	}
}
