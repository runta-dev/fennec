package launcher

import (
	"context"
	"fennec/internal/control"
	"fennec/internal/hookbridge"
	"fennec/internal/hookplugins"
	"path/filepath"
	"time"
)

type localHookClient struct {
	client                  control.Client
	runtime, session, lease string
}

func prepareLocalHooks(ctx context.Context, plan LaunchPlan, home string) (*localHookClient, error) {
	if plan.Agent != "codex" {
		return nil, nil
	}
	context := hookplugins.Capture()
	catalog, err := hookplugins.Catalog(home, context)
	if err != nil {
		return nil, err
	}
	if len(catalog) == 0 && (plan.Launch == nil || len(plan.Launch.Hooks) == 0) {
		return nil, nil
	}
	client := control.Client{Socket: filepath.Join(home, ".config/fennec/ipc/control.sock")}
	var reply struct {
		LeaseID string `json:"lease_id"`
	}
	if err = client.Call(ctx, "hooks.bind", map[string]any{"runtime_id": plan.RuntimeID, "session_id": plan.SessionID, "context": context}, &reply); err != nil {
		return nil, err
	}
	return &localHookClient{client: client, runtime: plan.RuntimeID, session: plan.SessionID, lease: reply.LeaseID}, nil
}
func (c *localHookClient) execute(ctx context.Context, reference string, input []byte) hookbridge.Result {
	if c == nil {
		return hookbridge.Unregistered()
	}
	step, cancel := context.WithTimeout(ctx, hookbridge.MaxCallDuration)
	defer cancel()
	var result hookbridge.Result
	if c.client.Call(step, "hooks.execute", hookbridge.Call{RuntimeID: c.runtime, SessionID: c.session, LeaseID: c.lease, BindingID: reference, Input: input}, &result) != nil || !result.Valid() {
		return hookbridge.Unregistered()
	}
	return result
}
func (c *localHookClient) close() {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.client.Call(ctx, "hooks.unbind", map[string]string{"lease_id": c.lease}, nil)
}
