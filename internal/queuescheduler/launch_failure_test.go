package queuescheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"fennec/internal/board"
)

// failedLaunch fails every start without ever creating a session, like a
// follow-up whose resume found no earlier conversation (FEN-11).
type failedLaunch struct{ err error }

func (e failedLaunch) List(context.Context) ([]Session, error) { return nil, nil }
func (e failedLaunch) Start(context.Context, board.Project, board.Request, board.Prompt) error {
	return e.err
}

// FEN-11: FEN-5's follow-up could not launch, was marked as awaiting input
// confirmation, and stayed there forever because no session ever existed to
// confirm or reconcile it.
func TestUnconfirmedRunWithoutASessionFails(t *testing.T) {
	for _, launchErr := range []error{
		errors.New("the previous conversation is not available to resume"),
		fmt.Errorf("%w: image upload was not confirmed", ErrInputUnconfirmed),
	} {
		store := board.Store{Home: t.TempDir(), Role: "cloud"}
		p, err := store.CreateProject(board.Project{Name: "Work", Queue: 1, Workers: []board.Worker{{Agent: "claude", On: true}}})
		if err != nil {
			t.Fatal(err)
		}
		if err = store.SetRouting(board.Routing{Locations: map[string]string{"claude": "cloud"}}); err != nil {
			t.Fatal(err)
		}
		r, _ := store.CreateRequest(p.ID, "me", "Delete threads", "", "")
		s := Scheduler{Store: store, Executor: failedLaunch{launchErr}}
		step(t, s)
		got, _ := store.Request(p.ID, r.Number)
		if got.Status != board.Unknown {
			t.Fatalf("%v: not unconfirmed: %+v", launchErr, got)
		}
		wantRun := "unknown"
		if errors.Is(launchErr, ErrInputUnconfirmed) {
			wantRun = "input_unknown"
		}
		if state := got.Runs[len(got.Runs)-1].State; state != wantRun {
			t.Fatalf("%v: run state %q, want %q", launchErr, state, wantRun)
		}
		// A launch may still be on its way into the listing.
		later(&s, time.Minute)
		step(t, s)
		if got, _ = store.Request(p.ID, r.Number); got.Status != board.Unknown {
			t.Fatalf("%v: failed too early: %+v", launchErr, got)
		}
		later(&s, StallAfter+time.Minute)
		step(t, s)
		if got, _ = store.Request(p.ID, r.Number); got.Status != board.Failed {
			t.Fatalf("%v: still awaiting confirmation: %+v", launchErr, got)
		}
	}
}
