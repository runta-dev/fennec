package plan

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/models"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
	"fennec/cua/internal/vision"
)

// clock is a fake clock whose sleeps advance it at once.
type clock2 struct {
	mu sync.Mutex
	t  time.Time
	// real, when set, also sleeps that long for real per sleep, so a real
	// context deadline can pass.
	real time.Duration
	// onSleep runs after each sleep.
	onSleep func()
}

func (c *clock2) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock2) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.t = c.t.Add(d)
	hook := c.onSleep
	c.mu.Unlock()
	if c.real > 0 {
		select {
		case <-time.After(c.real):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if hook != nil {
		hook()
	}
	return ctx.Err()
}

// desk wraps the fake desktop with hooks: a menu bar shown only when asked
// for, perform and driver hooks, and a terminal screen.
type desk struct {
	*fakedesk.Desk
	mu        sync.Mutex
	onPerform func(id, action string)
	onDriver  func(tool string, args map[string]any)
	onClose   func(key string) bool     // false keeps the window open
	onSet     func(id, v string) string // what the field holds after a set
	// screen, when set, answers Focused for screenPID with a node whose
	// value is the screen.
	screen    *string
	screenPID int
	trees     int
	// onRead runs before each element read, outside the desk's lock; cut
	// marks elements whose reads report a cut value.
	onRead func(id string, full, tail bool)
	cut    map[string]bool
}

func (d *desk) Perform(ctx context.Context, h view.Handle, action string) error {
	err := d.Desk.Perform(ctx, h, action)
	if d.onPerform != nil {
		id, _ := h.(string)
		d.onPerform(id, action)
	}
	return err
}

func (d *desk) Driver(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	raw, err := d.Desk.Driver(ctx, tool, args)
	if d.onDriver != nil {
		d.onDriver(tool, args)
	}
	return raw, err
}

func (d *desk) CloseWindow(ctx context.Context, w view.RawWindow) error {
	if d.onClose != nil && !d.onClose(w.Key) {
		d.Desk.Closed = append(d.Desk.Closed, w.Key)
		return nil
	}
	return d.Desk.CloseWindow(ctx, w)
}

// Tree leaves out the menu bar unless asked for it.
func (d *desk) Tree(ctx context.Context, w view.RawWindow, opts view.TreeOptions) (view.Tree, error) {
	d.mu.Lock()
	d.trees++
	d.mu.Unlock()
	t, err := d.Desk.Tree(ctx, w, opts)
	if err != nil || opts.Menus {
		return t, err
	}
	var keep []view.RawNode
	index := map[int]int{}
	for i, n := range t.Nodes {
		inBar := false
		for p := i; p >= 0; p = t.Nodes[p].Parent {
			if t.Nodes[p].Role == "menubar" {
				inBar = true
				break
			}
		}
		if inBar {
			continue
		}
		index[i] = len(keep)
		if n.Parent >= 0 {
			n.Parent = index[n.Parent]
		}
		keep = append(keep, n)
	}
	t.Nodes = keep
	return t, nil
}

func (d *desk) Focused(ctx context.Context, pid int) (*view.RawNode, error) {
	d.mu.Lock()
	s := d.screen
	spid := d.screenPID
	d.mu.Unlock()
	if s != nil && pid == spid {
		v := *s
		return &view.RawNode{Handle: "screen", Role: "textbox", Value: &v, States: []string{"focused"}}, nil
	}
	return d.Desk.Focused(ctx, pid)
}

func (d *desk) Read(ctx context.Context, h view.Handle, full, tail bool) (view.RawNode, bool, error) {
	if id, _ := h.(string); id == "screen" {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.screen == nil {
			return view.RawNode{}, false, nil
		}
		v := *d.screen
		if tail && len(v) > view.MaxText {
			v = v[len(v)-view.MaxText:]
		}
		return view.RawNode{Handle: "screen", Role: "textarea", Value: &v}, false, nil
	}
	id, _ := h.(string)
	if d.onRead != nil {
		d.onRead(id, full, tail)
	}
	n, cut, err := d.Desk.Read(ctx, h, full, tail)
	return n, cut || d.cut[id], err
}

func (d *desk) setScreen(s string) {
	d.mu.Lock()
	d.screen = &s
	d.mu.Unlock()
}

func (d *desk) getScreen() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.screen == nil {
		return ""
	}
	return *d.screen
}

// fakeModels answers model calls from functions and counts them.
type fakeModels struct {
	mu      sync.Mutex
	decide  func(args models.DecideArgs) (*models.Decided, error)
	ground  func(args models.GroundArgs) (*models.Grounded, error)
	ask     func(args models.AskArgs) (*models.Asked, error)
	decides []models.DecideArgs
	grounds int
	asks    int
}

func (m *fakeModels) Decide(ctx context.Context, args models.DecideArgs) (*models.Decided, error) {
	m.mu.Lock()
	m.decides = append(m.decides, args)
	f := m.decide
	m.mu.Unlock()
	if f == nil {
		return nil, &models.Error{Code: protocol.ErrModelUnavailable, Message: "no decide"}
	}
	return f(args)
}

func (m *fakeModels) Ground(ctx context.Context, args models.GroundArgs) (*models.Grounded, error) {
	m.mu.Lock()
	m.grounds++
	f := m.ground
	m.mu.Unlock()
	if f == nil {
		return nil, &models.Error{Code: protocol.ErrModelUnavailable, Message: "no ground"}
	}
	return f(args)
}

func (m *fakeModels) Ask(ctx context.Context, args models.AskArgs) (*models.Asked, error) {
	m.mu.Lock()
	m.asks++
	f := m.ask
	m.mu.Unlock()
	if f == nil {
		return nil, &models.Error{Code: protocol.ErrModelUnavailable, Message: "no ask"}
	}
	return f(args)
}

func (m *fakeModels) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.decides) + m.grounds + m.asks
}

// noul answers a yes/no decide with p.
func noul(p float64) *models.Decided {
	return &models.Decided{Answers: map[string]models.Answer{"hit": {Type: models.Noul, Probability: p}}, Served: protocol.Served{Role: "verify", Model: "fake-verify"}}
}

// choice answers a pick with probabilities by ref.
func choice(probs map[string]float64) *models.Decided {
	best, bp := "", -1.0
	for k, p := range probs {
		if p > bp {
			best, bp = k, p
		}
	}
	return &models.Decided{Answers: map[string]models.Answer{"pick": {Type: models.Choice, Choice: best, Confidence: bp, Probabilities: probs}}, Served: protocol.Served{Role: "decide", Model: "fake-decide"}}
}

// fakeScreen answers one frame.
type fakeScreen struct {
	mu    sync.Mutex
	shot  Shot
	shots int
}

func (s *fakeScreen) Shot(ctx context.Context) (*Shot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shots++
	cp := s.shot
	cp.Frame = s.shot.Frame.Clone()
	return &cp, nil
}

func newScreen(w, h int) *fakeScreen {
	return &fakeScreen{shot: Shot{Frame: vision.NewFrame(w, h), Scale: 1}}
}

// fakeObserver answers failure views.
type fakeObserver struct {
	res  *protocol.ObserveResult
	reqs []protocol.ObserveRequest
}

func (o *fakeObserver) Observe(ctx context.Context, req protocol.ObserveRequest) (*protocol.ObserveResult, error) {
	o.reqs = append(o.reqs, req)
	cp := *o.res
	return &cp, nil
}

type rig struct {
	t     *testing.T
	d     *desk
	lists *actions.Lists
	p     *policy.Policy
	x     *actions.Executor
	e     *Executor
	clk   *clock2
	m     *fakeModels
}

// newRig makes an executor over an empty fake desktop with the policy and
// a fake clock; the user is idle.
func newRig(t *testing.T, cfg policy.Config) *rig {
	t.Helper()
	p, err := policy.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock2{t: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
	p.Activity.Idle = func() time.Duration { return time.Hour }
	p.Activity.Now = clk.now
	d := &desk{Desk: fakedesk.New()}
	lists := &actions.Lists{Now: clk.now}
	x := &actions.Executor{B: actions.Watch(d, lists), Refs: view.NewRegistry(0), Policy: p, Now: clk.now, Sleep: clk.sleep}
	e := &Executor{X: x, Now: clk.now, Sleep: clk.sleep}
	return &rig{t: t, d: d, lists: lists, p: p, x: x, e: e, clk: clk}
}

// notes adds TextEdit's "Notes" window (cg:1, pid 100) in front with a
// Save button.
func (r *rig) notes() *rig {
	r.d.Win("cg:1", "TextEdit", "Notes", 100, [4]int{0, 0, 800, 600}, true)
	r.d.Add("cg:1", "save", "button", "Save", fakedesk.Box(100, 100, 80, 20), "cg:1")
	return r
}

func (r *rig) models() *fakeModels {
	if r.m == nil {
		r.m = &fakeModels{}
		r.e.Models = r.m
	}
	return r.m
}

func (r *rig) run(steps ...protocol.Step) *protocol.ActResult {
	r.t.Helper()
	return r.act(protocol.ActRequest{Steps: steps})
}

func (r *rig) act(req protocol.ActRequest) *protocol.ActResult {
	r.t.Helper()
	res, perr := r.e.Run(context.Background(), &engine.Call{}, req, engine.PlanOptions{})
	if perr != nil {
		r.t.Fatalf("the plan did not run: %+v", perr)
	}
	return res
}

// tool runs the act tool on raw arguments.
func (r *rig) tool(args string) (*protocol.ActResult, *protocol.Error) {
	r.t.Helper()
	out, perr := r.e.Tool()(context.Background(), &engine.Call{}, json.RawMessage(args))
	if out == nil {
		return nil, perr
	}
	return out.(*protocol.ActResult), perr
}

// sel is a selector target.
func sel(s string) *protocol.Target { return &protocol.Target{Select: s} }

func str(s string) *string { return &s }

func raw(s string) *json.RawMessage { m := json.RawMessage(s); return &m }

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

// mustStop checks a plan stopped at step i with code.
func mustStop(t *testing.T, res *protocol.ActResult, i int, code protocol.ErrorCode) {
	t.Helper()
	if res.OK || res.StoppedAt == nil || *res.StoppedAt != i || res.Error == nil || res.Error.Code != code {
		t.Fatalf("want a stop at %d with %s, got ok=%v stopped=%v error=%+v steps=%s", i, code, res.OK, res.StoppedAt, res.Error, jsonOf(res.Steps))
	}
}

func mustPass(t *testing.T, res *protocol.ActResult) {
	t.Helper()
	if !res.OK {
		t.Fatalf("the plan stopped: %+v at %v; steps %s", res.Error, res.StoppedAt, jsonOf(res.Steps))
	}
}

func tools(d *desk) string { return strings.Join(d.Tools(), ",") }

func (d *desk) SetValue(ctx context.Context, h view.Handle, value string) error {
	if d.onSet != nil {
		id, _ := h.(string)
		value = d.onSet(id, value)
	}
	return d.Desk.SetValue(ctx, h, value)
}

func engineOpts() engine.PlanOptions { return engine.PlanOptions{} }

func (d *desk) Select(ctx context.Context, h view.Handle) error {
	err := d.Desk.Select(ctx, h)
	if d.onPerform != nil {
		id, _ := h.(string)
		d.onPerform(id, "select")
	}
	return err
}

type viewNode = view.RawNode

type ctxT = context.Context
