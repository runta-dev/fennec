package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/account"
	"fennec/internal/hookbridge"
	"strings"
	"testing"
)

func TestHookLeaseRejectsWrongRuntimeSessionAndUnbind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := account.New(ctx, serviceCredentials{}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	lifecycle, _, epoch, _ := manager.Session()
	s := newDaemonServices(t.TempDir(), manager)
	s.ctx = lifecycle
	s.epoch = epoch
	s.user = "owner"
	s.config = brokerConfiguration{RuntimeID: "runtime"}
	session := strings.Repeat("a", 32)
	raw, _ := json.Marshal(map[string]any{"runtime_id": "runtime", "session_id": session})
	reply, err := s.bindHooks(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	id := reply.(map[string]string)["lease_id"]
	lease := s.hookLeases[id]
	for _, call := range []hookbridge.Call{{LeaseID: id, RuntimeID: "wrong", SessionID: session}, {LeaseID: id, RuntimeID: "runtime", SessionID: strings.Repeat("b", 32)}, {LeaseID: id, RuntimeID: "runtime", SessionID: session, BindingID: strings.Repeat("f", 64)}} {
		raw, _ = json.Marshal(call)
		result, err := s.executeHook(ctx, raw)
		if err != nil || result.(hookbridge.Result).Status != "unregistered" {
			t.Fatal(result, err)
		}
	}
	raw, _ = json.Marshal(map[string]string{"lease_id": id})
	if _, err = s.hookHandlers()["hooks.unbind"](ctx, raw); err != nil {
		t.Fatal(err)
	}
	if lease.lifetime.Err() == nil || len(s.hookLeases) != 0 {
		t.Fatal("unbind did not revoke lease")
	}
}
