package cli

import (
	"context"
	"errors"
	"fennec/internal/account"
	"fennec/internal/byoc"
	"fennec/internal/byoc/byoctest"
	"io"
	"testing"
	"time"
)

// A paired principal whose pairing record is gone fails preparation locally:
// it never falls back to the Runta API (the endpoint here is unreachable).
func TestSelfHostedPreparationRequiresPairingRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	home := t.TempDir()
	t.Setenv("HOME", home)
	manager := account.New(ctx, serviceCredentials{}, "http://127.0.0.1:1")
	if _, err := manager.Pair(byoctest.Machine); err != nil {
		t.Fatal(err)
	}
	_, principal, _, ok := manager.Session()
	if !ok {
		t.Fatal("paired principal unavailable")
	}
	s := newDaemonServices(home, manager)
	if err := s.prepare(ctx, principal, io.Discard); !errors.Is(err, byoc.ErrNotPaired) {
		t.Fatalf("unexpected preparation result: %v", err)
	}
}

func TestNewCloudComputerFollowsPrincipal(t *testing.T) {
	home := t.TempDir()
	runta, err := newCloudComputer(home, account.Principal{Kind: account.PrincipalRunta, Token: "tok"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runta.(runtaComputer); !ok || runta.Scope() != "tok" {
		t.Fatal("Runta principal did not select the Runta computer")
	}
	self, err := newCloudComputer(home, account.Principal{Kind: account.PrincipalBYOC, Machine: byoctest.Machine}, false)
	if err != nil {
		t.Fatal(err)
	}
	if self.Scope() != "byoc:"+byoctest.Machine || self.Directory() != byoc.Dir(home) {
		t.Fatalf("unexpected self-hosted computer scope %q dir %q", self.Scope(), self.Directory())
	}
	if _, err := newCloudComputer(home, account.Principal{}, false); err == nil {
		t.Fatal("selected a computer without a principal")
	}
}

func TestSelfHostedComputerResolvesOnlyThePairedMachine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	home := t.TempDir()
	computer := byoctest.New(t)
	computer.Pair(t, home)
	self := selfHostedComputer{home: home, machine: byoctest.Machine}

	var stages []string
	var resolved resolvedComputer
	connection, got, err := self.Resolve(ctx, func(stage, id, _ string) { stages = append(stages, stage+":"+id) }, func(r resolvedComputer) error {
		resolved = r
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	want := resolvedComputer{ID: byoctest.Machine, Owner: "byoc:" + byoctest.Machine}
	if got != want || resolved != want {
		t.Fatalf("resolved %+v / %+v", got, resolved)
	}
	if len(stages) != 1 || stages[0] != "connecting:"+byoctest.Machine {
		t.Fatalf("unexpected progress %q", stages)
	}

	if err := self.Authorize(ctx, byoctest.Machine); err != nil {
		t.Fatal(err)
	}
	reconnected, err := self.Reconnect(ctx, byoctest.Machine)
	if err != nil {
		t.Fatal(err)
	}
	reconnected.Close()

	// A session for another machine, or a pairing that now names another
	// machine, is refused before any connection.
	if err := self.Authorize(ctx, "1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"); !errors.Is(err, errPairedComputerChanged) {
		t.Fatalf("authorized another machine: %v", err)
	}
	stale := selfHostedComputer{home: home, machine: "1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"}
	if _, _, err := stale.Resolve(ctx, func(string, string, string) {}, func(resolvedComputer) error { return nil }); !errors.Is(err, errPairedComputerChanged) {
		t.Fatalf("resolved a stale pairing: %v", err)
	}

	// A refused verification callback stops before connecting.
	refused := errors.New("owner changed")
	if _, _, err := self.Resolve(ctx, func(string, string, string) {}, func(resolvedComputer) error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("resolve ignored a refused owner: %v", err)
	}

	// A replaced volume fails reconnection with an identity mismatch.
	computer.SetIdentity(`{"machine_id":"1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"}`)
	if _, err := self.Reconnect(ctx, byoctest.Machine); !errors.Is(err, byoc.ErrIdentityMismatch) {
		t.Fatalf("reconnected to a replaced machine: %v", err)
	}
}
