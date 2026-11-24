package controlplane

import (
	"context"
	"encoding/json"
	"fennec/internal/account"
	"fennec/internal/connectivity"
	"fennec/internal/runta"
	"io"
	"testing"
	"time"
)

// A paired self-hosted computer drives the same preparation lifecycle as a
// Runta account, and reaches Prepare as a BYOC principal without a token.
func TestPairedPrincipalDrivesPreparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manager := account.New(ctx, &testCredentials{}, "")
	seen := make(chan account.Principal, 4)
	c := &Controller{
		Account: manager, Connectivity: connectivity.NewMonitor(func() bool { return true }, time.Hour),
		Prepare: func(_ context.Context, principal account.Principal, _ io.Writer) error {
			seen <- principal
			return nil
		},
	}
	c.Start(ctx)
	if _, err := manager.Pair("vps-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case principal := <-seen:
		if principal.Kind != account.PrincipalBYOC || principal.Machine != "vps-1" || principal.Token != "" {
			t.Fatalf("unexpected principal: kind=%q machine=%q", principal.Kind, principal.Machine)
		}
	case <-ctx.Done():
		t.Fatal("paired principal did not start preparation")
	}
	for c.runtimeSnapshot().Stage != "ready" {
		select {
		case <-ctx.Done():
			t.Fatal("paired preparation did not become ready")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A 401 from Runta can only reject a Runta credential; it must never end a
// self-hosted pairing.
func TestRuntaRejectionDoesNotEndPairing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manager := account.New(ctx, &testCredentials{}, "")
	epoch, err := manager.Pair("vps-2")
	if err != nil {
		t.Fatal(err)
	}
	c := &Controller{Account: manager, Prepare: func(context.Context, account.Principal, io.Writer) error {
		return &runta.APIError{Status: 401}
	}}
	if _, err := c.ensureRuntime(ctx, json.RawMessage(nil)); err != nil {
		t.Fatal(err)
	}
	for c.runtimeSnapshot().Running {
		select {
		case <-ctx.Done():
			t.Fatal("preparation did not finish")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, principal, current, ok := manager.Session(); !ok || current != epoch || principal.Machine != "vps-2" {
		t.Fatal("a Runta 401 ended the self-hosted pairing")
	}
}
