package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
	"fennec/cua/internal/view"
)

func actRig(t *testing.T, cfg policy.Config) (*fakedesk.Desk, Tool, *policy.Policy) {
	t.Helper()
	d := fakedesk.New()
	d.Win("cg:10", "TextEdit", "Notes", 100, [4]int{0, 0, 800, 600}, true)
	d.Add("cg:10", "save", "button", "Save", fakedesk.Box(100, 100, 80, 20), "cg:10")
	p, err := policy.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Activity.Idle = func() time.Duration { return time.Hour }
	x := &actions.Executor{B: d, Refs: view.NewRegistry(0), Policy: p, Sleep: func(context.Context, time.Duration) error { return nil }}
	return d, ActTool(x, p, nil), p
}

func turnCall(turn *sched.Turn, owner string) *Call {
	return &Call{Turn: func(ctx context.Context) (func(), error) { return turn.Acquire(ctx, owner) }}
}

func TestActRunsStepsUntilTheFirstFailure(t *testing.T) {
	d, act, _ := actRig(t, policy.Config{})
	args := `{"steps":[{"press":"button \"Save\""},{"key":"cmd+nonsense"},{"type":"{{pw}}","secret":true}],"params":{"pw":"hunter2"}}`
	out, perr := act(context.Background(), turnCall(&sched.Turn{}, "a"), json.RawMessage(args))
	if perr != nil {
		t.Fatal(perr)
	}
	res := out.(protocol.ActResult)
	if res.OK || res.StoppedAt == nil || *res.StoppedAt != 1 || len(res.Steps) != 2 || !res.Steps[0].OK {
		t.Fatalf("%+v", res)
	}
	if res.Error == nil || res.Error.Code != protocol.ErrInvalid || len(res.Remaining) != 2 {
		t.Fatalf("%+v", res)
	}
	if *res.Remaining[1].Type != "[secret]" {
		t.Fatalf("a secret in the remaining steps: %q", *res.Remaining[1].Type)
	}
	if len(d.Calls) != 0 || strings.Join(d.Performs, ",") != "save:AXPress" {
		t.Fatalf("%s", d)
	}
}

func TestActHoldsTheTurnAndAsksAfterIt(t *testing.T) {
	_, act, p := actRig(t, policy.Config{Confirm: "coordinate"})
	turn := &sched.Turn{Busy: 20 * time.Millisecond}
	asked := ""
	p.Gate.Confirmer = confirmFunc(func(q string) bool { asked = turn.Holder(); return false })
	_, perr := act(context.Background(), turnCall(turn, "a"), json.RawMessage(`{"steps":[{"click":{"point":{"x":1,"y":2}}}]}`))
	if perr == nil || perr.Code != protocol.ErrDeclined || asked != "a" {
		t.Fatalf("%+v asked while %q held the turn", perr, asked)
	}
	release, _ := turn.Acquire(context.Background(), "b")
	defer release()
	asked = ""
	_, perr = act(context.Background(), turnCall(turn, "a"), json.RawMessage(`{"steps":[{"click":{"point":{"x":1,"y":2}}}]}`))
	if perr == nil || perr.Code != protocol.ErrBusy || asked != "" {
		t.Fatalf("%+v; asked %q", perr, asked)
	}
}

func TestActLeavesPlanStepsToThePlanExecutor(t *testing.T) {
	d, act, _ := actRig(t, policy.Config{})
	_, perr := act(context.Background(), turnCall(&sched.Turn{}, "a"), json.RawMessage(`{"steps":[{"press":"e1","expect":{"window_gone":"Save"}}]}`))
	if perr == nil || perr.Code != protocol.ErrUnavailable || len(d.Performs) != 0 {
		t.Fatalf("%+v", perr)
	}
}

type confirmFunc func(string) bool

func (f confirmFunc) Confirm(_ context.Context, q string) (bool, error) { return f(q), nil }
