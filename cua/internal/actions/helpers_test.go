package actions_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// clock is a fake clock whose sleeps advance it at once.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return nil
}

func (c *clock) advance(d time.Duration) { _ = c.sleep(context.Background(), d) }

type rig struct {
	t   *testing.T
	d   *fakedesk.Desk
	x   *actions.Executor
	clk *clock
}

// newRig makes an executor over d with the input mode and a fake clock; the
// user is idle.
func newRig(t *testing.T, d *fakedesk.Desk, input string) *rig {
	t.Helper()
	return newRigPolicy(t, d, policy.Config{Input: input})
}

func newRigPolicy(t *testing.T, d *fakedesk.Desk, cfg policy.Config) *rig {
	t.Helper()
	p, err := policy.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
	p.Activity.Idle = func() time.Duration { return time.Hour }
	p.Activity.Now = clk.now
	x := &actions.Executor{B: d, Refs: view.NewRegistry(0), Policy: p, Now: clk.now, Sleep: clk.sleep}
	return &rig{t: t, d: d, x: x, clk: clk}
}

func (r *rig) step(s protocol.Step) protocol.StepResult {
	r.t.Helper()
	return r.x.Step(context.Background(), actions.Request{}, s, 0)
}

func (r *rig) stepIn(window string, s protocol.Step) protocol.StepResult {
	r.t.Helper()
	return r.x.Step(context.Background(), actions.Request{Window: window}, s, 0)
}

// ref hands out the ref of a window or element as an observe would.
func (r *rig) ref(winKey, id string) string {
	r.t.Helper()
	walk := r.x.Refs.Begin()
	defer walk.End()
	ws, _ := r.d.Windows(context.Background())
	refs := walk.Windows(ws)
	for i, w := range ws {
		if w.Key != winKey {
			continue
		}
		if id == "" {
			return refs[i]
		}
		for _, n := range r.d.Trees[winKey] {
			if n.Handle == id {
				return walk.Element(refs[i], winKey, n)
			}
		}
	}
	r.t.Fatalf("no %s in %s", id, winKey)
	return ""
}

func tgt(s string) *protocol.Target {
	t := protocol.TargetFromString(s)
	return &t
}

func at(x, y float64) *protocol.Target {
	return &protocol.Target{Point: &protocol.Point{X: x, Y: y}}
}

func winAt(x, y float64, window string) *protocol.Target {
	return &protocol.Target{Point: &protocol.Point{X: x, Y: y, Space: protocol.SpaceWindow}, Window: window}
}

func text(s string) *string { return &s }

func raw(v any) *json.RawMessage {
	b, _ := json.Marshal(v)
	m := json.RawMessage(b)
	return &m
}

func node(role, name, value string, states ...string) *view.RawNode {
	v := value
	return &view.RawNode{Role: role, Name: name, Value: &v, States: states}
}

// scene is TextEdit's "Notes" in front with a Save button and a Name field,
// and Finder's "Downloads" behind it, overlapping at the lower right.
func scene() *fakedesk.Desk {
	d := fakedesk.New()
	d.Win("cg:10", "TextEdit", "Notes", 100, [4]int{0, 0, 800, 600}, true)
	d.Add("cg:10", "save", "button", "Save", fakedesk.Box(100, 100, 80, 20), "cg:10")
	d.Add("cg:10", "name", "textbox", "Name", fakedesk.Box(100, 200, 200, 20), "cg:10")
	d.Add("cg:10", "tab", "tab", "General", fakedesk.Box(300, 40, 80, 20), "cg:10")
	d.Win("cg:20", "Finder", "Downloads", 200, [4]int{400, 300, 600, 400}, false)
	d.Add("cg:20", "trash", "button", "Trash", fakedesk.Box(800, 500, 40, 20), "cg:20")
	d.Hits[[2]int{140, 110}] = fakedesk.Hit{Node: "save", Win: "cg:10"}
	d.Hits[[2]int{200, 210}] = fakedesk.Hit{Node: "name", Win: "cg:10"}
	return d
}

func wantStatus(t *testing.T, r protocol.StepResult, status string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %s, want %s: %+v (error %+v)", r.Status, status, r, r.Error)
	}
}

func wantCode(t *testing.T, r protocol.StepResult, code protocol.ErrorCode) {
	t.Helper()
	if r.Error == nil || r.Error.Code != code {
		t.Fatalf("error %+v, want code %s (result %+v)", r.Error, code, r)
	}
}

func wantNoInput(t *testing.T, d *fakedesk.Desk) {
	t.Helper()
	if len(d.Calls) != 0 || len(d.HitCalls) != 0 || len(d.Performs) != 0 || len(d.Raised) != 0 {
		t.Fatalf("input was sent:\n%s", d)
	}
}

func notesHave(r protocol.StepResult, part string) bool {
	for _, n := range r.Notes {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}

func calls(d *fakedesk.Desk, tool string) []fakedesk.Call {
	var out []fakedesk.Call
	for _, c := range d.Calls {
		if c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

func joined(tools []string) string { return strings.Join(tools, ",") }

func retryFalse(r protocol.StepResult) bool { return r.Retry != nil && !*r.Retry }

func newRigPolicyLocked(t *testing.T, d *fakedesk.Desk) *rig {
	t.Helper()
	return newRigPolicy(t, d, policy.Config{Input: protocol.InputBackground, LockedInput: true})
}
