package cli

import (
	"context"
	"encoding/json"
	"testing"
)

func TestVerifiedOwnerCannotChangeWithinAccountEpoch(t *testing.T) {
	s := &daemonServices{epoch: 1, ctx: context.Background(), user: "owner-a"}
	for _, user := range []string{"", "owner-b"} {
		if err := s.initialize(context.Background(), "token", 1, user); err == nil {
			t.Fatalf("accepted owner %q", user)
		}
	}
	if err := s.initialize(context.Background(), "token", 1, "owner-a"); err != nil {
		t.Fatal(err)
	}
}

func TestPreparationProgressBeforeOwnerValidationDoesNotInitializeServices(t *testing.T) {
	s := &daemonServices{epoch: 1}
	s.runtimeEvent(1, map[string]json.RawMessage{"type": json.RawMessage(`"runtime_progress"`), "runtime_id": json.RawMessage(`"unverified-hint"`)})
	if s.config.RuntimeID != "" || s.ctx != nil {
		t.Fatal("early progress initialized account services")
	}
}
