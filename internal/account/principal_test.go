package account

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRuntaSessionReportsRuntaPrincipal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := New(ctx, &memoryCredentials{token: "secret-token"}, "")
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	_, principal, _, ok := m.Session()
	if !ok || principal.Kind != PrincipalRunta || principal.Token != "secret-token" || principal.Machine != "" {
		t.Fatalf("unexpected principal: kind=%q machine=%q ok=%v", principal.Kind, principal.Machine, ok)
	}
	if token, isRunta := principal.RuntaToken(); !isRunta || token != "secret-token" {
		t.Fatal("Runta principal did not expose its token")
	}
	if !m.Snapshot().Active() {
		t.Fatal("signed-in snapshot is not active")
	}
}

func TestPairSwitchesPrincipalUnderFreshEpoch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := &memoryCredentials{token: "secret-token"}
	m := New(ctx, store, "")
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	runtaCtx, _, runtaEpoch, _ := m.Session()

	epoch, err := m.Pair("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if epoch <= runtaEpoch {
		t.Fatal("pairing did not start a new epoch")
	}
	if runtaCtx.Err() == nil {
		t.Fatal("Runta session lifecycle survived the principal switch")
	}
	pairedCtx, principal, current, ok := m.Session()
	if !ok || current != epoch || principal.Kind != PrincipalBYOC || principal.Machine != "00000000-0000-4000-8000-000000000001" || principal.Token != "" {
		t.Fatalf("unexpected principal: kind=%q machine=%q ok=%v", principal.Kind, principal.Machine, ok)
	}
	if !principal.IsBYOC() {
		t.Fatal("paired principal is not BYOC")
	}
	if _, isRunta := principal.RuntaToken(); isRunta {
		t.Fatal("BYOC principal exposed a Runta token")
	}
	if snapshot := m.Snapshot(); snapshot.Status != StatusPaired || !snapshot.Active() {
		t.Fatalf("unexpected snapshot status %q", snapshot.Status)
	}
	if token, _ := store.Read(ctx); token != "secret-token" {
		t.Fatal("pairing erased the stored Runta credential")
	}

	// Re-pairing the same machine is idempotent and keeps the epoch.
	if again, err := m.Pair("00000000-0000-4000-8000-000000000001"); err != nil || again != epoch || pairedCtx.Err() != nil {
		t.Fatal("re-pairing the same machine replaced the session")
	}
}

func TestPairRejectsInvalidMachineIdentity(t *testing.T) {
	m := New(context.Background(), &memoryCredentials{}, "")
	for _, id := range []string{"", "vps-1", "00000000-0000-4000-8000-00000000000G", "00000000-0000-4000-8000-0000000000001", "00000000-0000-4000-8000-00000000000A", "00000000000040008000000000000001", string(make([]byte, 36))} {
		if _, err := m.Pair(id); err == nil {
			t.Fatalf("accepted machine identity %q", id)
		}
	}
	if m.Snapshot().Status != "signed_out" {
		t.Fatal("rejected pairing changed account state")
	}
}

func TestUnpairIgnoresStaleMachine(t *testing.T) {
	m := New(context.Background(), &memoryCredentials{}, "")
	epoch, err := m.Pair("00000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	m.Unpair("00000000-0000-4000-8000-000000000001")
	if _, principal, current, ok := m.Session(); !ok || current != epoch || principal.Machine != "00000000-0000-4000-8000-000000000002" {
		t.Fatal("unpairing a different machine ended the current pairing")
	}
	m.Unpair("00000000-0000-4000-8000-000000000002")
	if _, _, current, ok := m.Session(); ok || current <= epoch {
		t.Fatal("unpairing did not end the session under a new epoch")
	}
	if m.Snapshot().Status != "signed_out" {
		t.Fatal("unpaired account is not signed out")
	}
}

func TestSignInReplacesPairedPrincipal(t *testing.T) {
	m := New(context.Background(), &memoryCredentials{}, "http://127.0.0.1:1")
	if _, err := m.Pair("00000000-0000-4000-8000-000000000003"); err != nil {
		t.Fatal(err)
	}
	if state := m.Login(); state.Status != "authorizing" {
		t.Fatalf("unexpected status %q", state.Status)
	}
	m.CancelAuthorization(context.Background())
	if _, principal, _, ok := m.Session(); ok || principal.Machine != "" {
		t.Fatal("paired machine survived a Runta sign-in attempt")
	}
}

func TestRuntaQueriesRequireRuntaPrincipal(t *testing.T) {
	m := New(context.Background(), &memoryCredentials{}, "")
	if _, err := m.Pair("00000000-0000-4000-8000-000000000004"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Profile(context.Background()); !errors.Is(err, ErrRuntaAccountRequired) {
		t.Fatalf("profile under BYOC: %v", err)
	}
	if _, err := m.Organizations(context.Background()); !errors.Is(err, ErrRuntaAccountRequired) {
		t.Fatalf("organizations under BYOC: %v", err)
	}
}
