package cli

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"fennec/internal/account"
	"fennec/internal/cloudstate"
	"fennec/internal/runta"
)

type presenceCall struct {
	epoch         uint64
	owner, target string
}

func presenceFixture(t *testing.T, gone func(id string) (bool, error)) (*daemonServices, context.Context, uint64, chan presenceCall, *atomic.Int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	manager := account.New(ctx, serviceCredentials{}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, epoch, _ := manager.Session()
	s := newDaemonServices(t.TempDir(), manager)
	s.user = "owner"
	s.epoch = epoch
	s.config.RuntimeID = "old"
	s.channel = &terminalChannel{} // no live transport: the resident channel is down
	calls := make(chan presenceCall, 4)
	s.onRuntimeDeleted = func(_ context.Context, epoch uint64, owner, runtimeID string) error {
		calls <- presenceCall{epoch, owner, runtimeID}
		return nil
	}
	checks := &atomic.Int64{}
	previousCheck, previousInterval := checkRuntimeGone, runtimePresenceInterval
	previousConfirmations, previousSpan := runtimeDeletionConfirmations, runtimeDeletionConfirmSpan
	checkRuntimeGone = func(_ context.Context, _, token, owner, id string) (bool, error) {
		checks.Add(1)
		if token != "test-token" || owner != "owner" {
			t.Errorf("unexpected token %q or owner %q", token, owner)
		}
		return gone(id)
	}
	runtimePresenceInterval = 5 * time.Millisecond
	runtimeDeletionConfirmations, runtimeDeletionConfirmSpan = 3, 20*time.Millisecond
	t.Cleanup(func() {
		checkRuntimeGone, runtimePresenceInterval = previousCheck, previousInterval
		runtimeDeletionConfirmations, runtimeDeletionConfirmSpan = previousConfirmations, previousSpan
	})
	return s, ctx, epoch, calls, checks
}

func TestRuntimePresenceReplacesComputerDeletedOutsideFennec(t *testing.T) {
	s, ctx, epoch, calls, _ := presenceFixture(t, func(id string) (bool, error) { return id == "old", nil })
	go s.runRuntimePresence(ctx, epoch)
	select {
	case call := <-calls:
		if call != (presenceCall{epoch, "owner", "old"}) {
			t.Fatalf("unexpected replacement %+v", call)
		}
		if gen := cloudstate.NewScoped(s.home, runta.ActiveEnvironment(s.home).Key()).Generation("owner"); gen != "old" {
			t.Fatalf("replacement must derive a fresh creation key, generation=%q", gen)
		}
	case <-ctx.Done():
		t.Fatal("deleted cloud computer was not replaced")
	}
}

func TestRuntimePresenceIgnoresOutagesAndExistingComputers(t *testing.T) {
	for name, gone := range map[string]func(string) (bool, error){
		"api error": func(string) (bool, error) { return false, errors.New("Runta HTTP 503") },
		"exists":    func(string) (bool, error) { return false, nil },
	} {
		t.Run(name, func(t *testing.T) {
			s, ctx, epoch, calls, checks := presenceFixture(t, gone)
			run, stop := context.WithCancel(ctx)
			defer stop()
			go s.runRuntimePresence(run, epoch)
			for checks.Load() < 3 && ctx.Err() == nil {
				time.Sleep(time.Millisecond)
			}
			select {
			case call := <-calls:
				t.Fatalf("replaced without an authoritative deletion: %+v", call)
			default:
			}
		})
	}
}

// A deletion made through Fennec keeps the cloud paused until the user chooses
// to create again; it must never be replaced automatically.
func TestRuntimePresenceLeavesFennecDeletionPaused(t *testing.T) {
	s, ctx, epoch, calls, checks := presenceFixture(t, func(string) (bool, error) { return true, nil })
	store := cloudstate.NewScoped(s.home, runta.ActiveEnvironment(s.home).Key())
	if err := store.Begin(cloudstate.Record{Owner: "owner", Runtime: "old", Request: "request", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	run, stop := context.WithCancel(ctx)
	defer stop()
	go s.runRuntimePresence(run, epoch)
	for checks.Load() < 3 && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	select {
	case call := <-calls:
		t.Fatalf("replaced a deletion made through Fennec: %+v", call)
	default:
	}
}

// A deletion answer interleaved with temporary conditions (a production deploy
// pausing the machine, a flaky API, the Mac's network) never accumulates into
// a replacement: each non-deletion answer starts the confirmation over.
func TestRuntimePresenceRequiresConsecutiveDeletionAnswers(t *testing.T) {
	var n atomic.Int64
	s, ctx, epoch, calls, checks := presenceFixture(t, func(string) (bool, error) {
		switch n.Add(1) % 3 {
		case 0:
			return false, errors.New("Runta HTTP 503")
		case 1:
			return true, nil
		default:
			return false, nil // paused or otherwise still present
		}
	})
	run, stop := context.WithCancel(ctx)
	defer stop()
	go s.runRuntimePresence(run, epoch)
	for checks.Load() < 30 && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	select {
	case call := <-calls:
		t.Fatalf("replaced on non-consecutive deletion answers: %+v", call)
	default:
	}
}

// Enough answers in quick succession are not enough: deletion must persist for
// the whole confirmation span.
func TestRuntimePresenceWaitsForConfirmationSpan(t *testing.T) {
	s, ctx, epoch, calls, checks := presenceFixture(t, func(string) (bool, error) { return true, nil })
	runtimeDeletionConfirmSpan = time.Hour
	run, stop := context.WithCancel(ctx)
	defer stop()
	go s.runRuntimePresence(run, epoch)
	for checks.Load() < 10 && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	select {
	case call := <-calls:
		t.Fatalf("replaced before the confirmation span elapsed: %+v", call)
	default:
	}
}
