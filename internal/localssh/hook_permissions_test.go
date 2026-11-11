package localssh

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fennec/internal/hookbridge"
)

type hookEvents chan Event

func (e hookEvents) Write(raw []byte) (int, error) {
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return 0, err
	}
	e <- event
	return len(raw), nil
}
func nextHookPrompt(t *testing.T, events hookEvents) Event {
	t.Helper()
	for {
		select {
		case e := <-events:
			if e.Type == "approval" {
				if e.Kind != "hook" {
					t.Fatal(e)
				}
				return e
			}
		case <-time.After(time.Second):
			t.Fatal("missing permission prompt")
			return Event{}
		}
	}
}

type permissionExecutor struct {
	ref   string
	calls int
	run   func(context.Context)
}

func (e *permissionExecutor) Binding() hookbridge.Binding {
	return hookbridge.Binding{ID: strings.Repeat("a", 64), Name: "fixture", Event: "PreToolUse", Policy: hookbridge.Required, TimeoutMS: 1000}
}
func (e *permissionExecutor) Fingerprint() (string, error) { return e.ref, nil }
func (e *permissionExecutor) Description() string          { return "fixture local hook" }
func (e *permissionExecutor) Execute(ctx context.Context, input []byte) hookbridge.Result {
	e.calls++
	if e.run != nil {
		e.run(ctx)
	}
	return hookbridge.Result{Stdout: input, Status: "executed"}
}
func startPermissionHook(ctx context.Context, b *Broker, e *permissionExecutor, owner string) <-chan hookbridge.Result {
	done := make(chan hookbridge.Result, 1)
	go func() {
		done <- hookbridge.Execute(ctx, owner, "runtime", e, []byte("payload"), func(prompt context.Context, description, scope string) (context.Context, func(), error) {
			return b.AuthorizeHook(prompt, ctx, description, scope)
		})
	}()
	return done
}
func awaitHook(t *testing.T, done <-chan hookbridge.Result) hookbridge.Result {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("hook did not finish")
		return hookbridge.Result{}
	}
}
func TestHookUsesExistingApprovalAndScopedAlwaysAllow(t *testing.T) {
	events := make(hookEvents, 64)
	b := NewBroker(t.TempDir(), events)
	defer b.shutdown()
	e := &permissionExecutor{ref: strings.Repeat("a", 64)}
	if err := b.remember("ssh-device-fingerprint"); err != nil {
		t.Fatal(err)
	}
	done := startPermissionHook(context.Background(), b, e, "owner")
	p := nextHookPrompt(t, events)
	select {
	case <-done:
		t.Fatal("executed without approval")
	default:
	}
	b.decide(Decision{ID: p.ID, Allow: true})
	if r := awaitHook(t, done); r.Status != "executed" || string(r.Stdout) != "payload" {
		t.Fatal(r)
	}
	done = startPermissionHook(context.Background(), b, e, "owner")
	p = nextHookPrompt(t, events)
	b.decide(Decision{ID: p.ID, Allow: true, NeverAsk: true})
	if r := awaitHook(t, done); r.Status != "executed" {
		t.Fatal(r)
	}
	done = startPermissionHook(context.Background(), b, e, "owner")
	if r := awaitHook(t, done); r.Status != "executed" {
		t.Fatal(r)
	}
	done = startPermissionHook(context.Background(), b, e, "other-owner")
	p = nextHookPrompt(t, events)
	b.decide(Decision{ID: p.ID, Allow: false})
	if r := awaitHook(t, done); r.Code != 2 {
		t.Fatal(r)
	}
	if e.calls != 3 {
		t.Fatal(e.calls)
	}
}
func TestHookChangedWhileApprovingNeverExecutes(t *testing.T) {
	events := make(hookEvents, 16)
	b := NewBroker(t.TempDir(), events)
	defer b.shutdown()
	e := &permissionExecutor{ref: strings.Repeat("a", 64)}
	done := startPermissionHook(context.Background(), b, e, "owner")
	p := nextHookPrompt(t, events)
	e.ref = strings.Repeat("b", 64)
	b.decide(Decision{ID: p.ID, Allow: true, NeverAsk: true})
	if r := awaitHook(t, done); r.Code != 2 || e.calls != 0 {
		t.Fatal(r, e.calls)
	}
}
func TestHookRevocationCancelsExecutionAndRequiresNewApproval(t *testing.T) {
	events := make(hookEvents, 32)
	b := NewBroker(t.TempDir(), events)
	defer b.shutdown()
	started := make(chan struct{})
	e := &permissionExecutor{ref: strings.Repeat("a", 64), run: func(ctx context.Context) { close(started); <-ctx.Done() }}
	done := startPermissionHook(context.Background(), b, e, "owner")
	p := nextHookPrompt(t, events)
	b.decide(Decision{ID: p.ID, Allow: true, NeverAsk: true})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("did not execute")
	}
	if err := b.clearTrust(); err != nil {
		t.Fatal(err)
	}
	if r := awaitHook(t, done); r.Code != 2 {
		t.Fatal(r)
	}
	done = startPermissionHook(context.Background(), b, e, "owner")
	p = nextHookPrompt(t, events)
	b.decide(Decision{ID: p.ID})
	if r := awaitHook(t, done); r.Code != 2 || e.calls != 1 {
		t.Fatal(r, e.calls)
	}
}
func TestHookCanceledPromptCannotExecuteLateDecision(t *testing.T) {
	events := make(hookEvents, 16)
	b := NewBroker(t.TempDir(), events)
	defer b.shutdown()
	e := &permissionExecutor{ref: strings.Repeat("a", 64)}
	ctx, cancel := context.WithCancel(context.Background())
	done := startPermissionHook(ctx, b, e, "owner")
	p := nextHookPrompt(t, events)
	cancel()
	b.decide(Decision{ID: p.ID, Allow: true, NeverAsk: true})
	if r := awaitHook(t, done); r.Code != 2 || e.calls != 0 {
		t.Fatal(r, e.calls)
	}
}
