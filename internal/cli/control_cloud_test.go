package cli

import (
	"context"
	"encoding/json"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"fennec/internal/account"
	"fennec/internal/cloudstate"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/runta"
)

type cloudBackendFixture struct {
	release chan struct{}
	entered chan struct{}
	calls   atomic.Int32
}

func (*cloudBackendFixture) CurrentUser(context.Context) (string, error) { return "owner", nil }
func (*cloudBackendFixture) Get(context.Context, string) (runta.Runtime, error) {
	return runta.Runtime{ID: "original", Owner: "owner", Revision: 3, Environment: map[string]string{"FENNEC_PURPOSE": "shared-v1"}}, nil
}
func (b *cloudBackendFixture) DeleteOwnedShared(ctx context.Context, id, owner string, rev uint64) error {
	b.calls.Add(1)
	close(b.entered)
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestCloudRPCReceiptIsNotDeletionCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager := account.New(ctx, serviceCredentials{}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, epoch, _ := manager.Session()
	services := newDaemonServices(t.TempDir(), manager)
	services.user = "owner"
	services.epoch = epoch
	services.config.RuntimeID = "original"
	c := &controlplane.Controller{Account: manager, Additional: map[string]control.Handler{}, Prepare: func(context.Context, string, io.Writer) error { return nil }}
	backend := &cloudBackendFixture{release: make(chan struct{}), entered: make(chan struct{})}
	h := installCloudControl(services.home, services, c, func(string) cloudstate.Backend { return backend })
	handlers := c.Handlers()
	raw, err := handlers["cloud.delete.preview"](ctx, json.RawMessage(`{"runtime_id":"original"}`))
	if err != nil {
		t.Fatal(err)
	}
	plan := raw.(cloudstate.Plan)
	confirmation, _ := json.Marshal(map[string]string{"plan_id": plan.ID, "confirm_runtime": "original"})
	receipt, err := handlers["cloud.delete"](ctx, confirmation)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.(map[string]any)["accepted"] != true {
		t.Fatal(receipt)
	}
	select {
	case <-backend.entered:
	case <-ctx.Done():
		t.Fatal("delete did not start")
	}
	status, err := handlers["cloud.status"](ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.(map[string]any)["stage"] != "deleting" || status.(map[string]any)["can_create"] != false {
		t.Fatal("receipt claimed completion", status)
	}
	if _, err = handlers["cloud.delete"](ctx, confirmation); err == nil {
		t.Fatal("duplicate mutation admitted")
	}
	create, _ := json.Marshal(map[string]string{"confirm_runtime": "original", "request_id": plan.Target.Request})
	if _, err = handlers["cloud.create"](ctx, create); err == nil {
		t.Fatal("creation admitted during deletion")
	}
	close(backend.release)
	for {
		status, err = handlers["cloud.status"](ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if status.(map[string]any)["stage"] == "deleted" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("missing completion")
		case <-time.After(time.Millisecond):
		}
	}
	if backend.calls.Load() != 1 {
		t.Fatal("duplicate DELETE")
	}
	if manager.Snapshot().Status != "authorized" {
		t.Fatal("local login was removed")
	}
	if _, err = handlers["cloud.create"](ctx, create); err != nil {
		t.Fatal(err)
	}
	if err = h.flow.Store.Check("owner"); err != nil {
		t.Fatal(err)
	}
}

func TestCloudDeletionDoesNotPauseAnotherActiveRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	manager := account.New(ctx, serviceCredentials{}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	lifecycle, _, epoch, _ := manager.Session()
	services := newDaemonServices(t.TempDir(), manager)
	services.user = "owner"
	services.epoch = epoch
	services.ctx = lifecycle
	services.config.RuntimeID = "replacement"
	c := &controlplane.Controller{Account: manager, Additional: map[string]control.Handler{}}
	backend := &cloudBackendFixture{release: make(chan struct{}), entered: make(chan struct{})}
	h := installCloudControl(services.home, services, c, func(string) cloudstate.Backend { return backend })
	plan, err := h.flow.Preview(ctx, backend, "original")
	if err != nil {
		t.Fatal(err)
	}
	work := context.WithValue(ctx, cloudEpochKey{}, epoch)
	if _, err = h.flow.Delete(work, backend, plan.ID, "original"); err == nil {
		t.Fatal("stale runtime confirmation accepted")
	}
	if backend.calls.Load() != 0 || lifecycle.Err() != nil {
		t.Fatal("stale preview disrupted current work")
	}
	if err = h.flow.Store.Check("owner"); err != nil {
		t.Fatal("stale preview blocked current cloud", err)
	}
}
