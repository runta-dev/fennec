package setup

import (
	"context"
	"errors"
	"testing"
)

type backend struct {
	configured bool
	installs   int
	fail       bool
}

func (b *backend) EnsureRuntime(context.Context, string) (string, error) { return "shared", nil }
func (b *backend) WaitReady(context.Context, string) error               { return nil }
func (b *backend) SyncManifest(context.Context, string, []Agent) error   { return nil }
func (b *backend) InstallAgent(context.Context, string, Agent) error     { b.installs++; return nil }
func (b *backend) InstallMagpie(context.Context, string) error           { return nil }
func (b *backend) ConfigureMagpie(context.Context, string, []Agent) error {
	if b.fail {
		return errors.New("provider authorization missing")
	}
	b.configured = true
	return nil
}
func (b *backend) Verify(context.Context, string, []Agent) error {
	if !b.configured {
		return errors.New("not configured")
	}
	return nil
}
func TestSharedSetup(t *testing.T) {
	b := &backend{}
	c := Coordinator{Backend: b}
	var last State
	err := c.Prepare(context.Background(), "user", []Agent{{"claude", "1.0.0"}, {"codex", "2.0.0"}}, func(s State) { last = s })
	if err != nil || last.Stage != "ready" || last.Completed != last.Total || b.installs != 2 || last.RuntimeID != "shared" {
		t.Fatalf("unexpected result: %+v %v", last, err)
	}
}
func TestMissingProviderNeverReady(t *testing.T) {
	c := Coordinator{Backend: &backend{fail: true}}
	var last State
	if c.Prepare(context.Background(), "user", nil, func(s State) { last = s }) == nil || last.Stage != "failed" {
		t.Fatalf("must fail: %+v", last)
	}
}
func TestVersionRequired(t *testing.T) {
	c := Coordinator{Backend: &backend{}}
	if c.Prepare(context.Background(), "user", []Agent{{ID: "codex"}}, nil) == nil {
		t.Fatal("accepted unknown version")
	}
}
