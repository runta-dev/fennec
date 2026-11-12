//go:build scenario

package scenario

import (
	"context"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/parity"
)

// countCommands counts menu commands whose name contains part.
func countCommands(st State, part string) int {
	n := 0
	for _, c := range st.Commands() {
		if strings.Contains(c, part) {
			n++
		}
	}
	return n
}

// CAP-STP-003, CAP-RGB-021: a menu toggle step run twice presses once.
func TestMenuToggleIdempotent(t *testing.T) {
	step := M{"menu": "View > Show Status Bar", "value": true}
	sstep := M{"menu": "View > Show Status Bar", "on": true}
	run(t, Case{
		Caps:    []string{"CAP-STP-003", "CAP-RGB-021"},
		Scene:   "menus",
		Fennec:  chain(fennecPlan(step), fennecPlan(step)),
		Shuttle: chain(shuttlePlan(sstep), shuttlePlan(sstep)),
		Keys:    []string{"menu.status_bar", "commands"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("menu.status_bar", r.State.Bool("menu.status_bar"), true)
			c.eq("Show Status Bar presses", countCommands(r.State, "status-bar"), 1)
			c.that(r.Outcome.OK, "a plan failed: %s", r.Outcome.Detail)
			return c.err()
		},
	})
}

// CAP-RGB-021: Show Ruler starts on with a stale off mark; asking for on
// presses nothing.
func TestMenuToggleAfterLaunch(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-021"},
		Scene:   "menus",
		Fennec:  fennecPlan(M{"menu": "View > Show Ruler", "value": true}),
		Shuttle: shuttlePlan(M{"menu": "View > Show Ruler", "on": true}),
		Keys:    []string{"menu.ruler", "commands"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("menu.ruler", r.State.Bool("menu.ruler"), true)
			c.eq("Show Ruler presses", countCommands(r.State, "ruler"), 0)
			c.that(r.Outcome.OK, "the plan failed: %s", r.Outcome.Detail)
			return c.err()
		},
	})
}

// CAP-RGA-040: a menu toggle reads its state back after the press and says
// so.
func TestMenuToggleReadBack(t *testing.T) {
	run(t, Case{
		Caps:  []string{"CAP-RGA-040"},
		Scene: "menus",
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			a, err := s.Steps(ctx, M{"menu": "View > Show Status Bar", "value": true})
			if err != nil {
				return parity.Outcome{}, err
			}
			o := a.Outcome()
			if a.OK() && a.Step(0).Status != "confirmed" {
				o.OK, o.Class, o.Detail = false, "unconfirmed", "the toggle was not read back: "+a.Step(0).Evidence
			}
			return o, nil
		},
		Shuttle: shuttlePlan(M{"menu": "View > Show Status Bar", "on": true}),
		Keys:    []string{"menu.status_bar", "commands"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("menu.status_bar", r.State.Bool("menu.status_bar"), true)
			c.that(r.Outcome.OK, "the plan failed: %s %s", r.Outcome.Class, r.Outcome.Detail)
			return c.err()
		},
	})
}

// CAP-STP-011: an entry that retitles itself (Wrap to Page, then Wrap to
// Window) is matched by its current title.
func TestMenuRetitledEntry(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-STP-011"},
		Scene:   "menus",
		Fennec:  chain(fennecPlan(M{"menu": "Format > Wrap to Page"}), fennecPlan(M{"menu": "Format > Wrap to Window"})),
		Shuttle: chain(shuttlePlan(M{"menu": "Format > Wrap to Page"}), shuttlePlan(M{"menu": "Format > Wrap to Window"})),
		Keys:    []string{"menu.wrap", "commands"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("menu.wrap", r.State.Str("menu.wrap"), "window")
			c.eq("wrap commands", countCommands(r.State, "wrap/"), 2)
			for _, cmd := range r.State.Commands() {
				c.that(!strings.HasSuffix(cmd, "/shortcut"), "a shortcut ran in place of a press: %s", cmd)
			}
			c.that(r.Outcome.OK, "a plan failed: %s", r.Outcome.Detail)
			return c.err()
		},
	})
}

// CAP-STP-033: a Windows path arrives unchanged.
func TestTypeBackslashes(t *testing.T) {
	const path = `C:\Users\fennec\new folder\report.txt`
	run(t, Case{
		Caps:    []string{"CAP-STP-033"},
		Scene:   "form",
		Fennec:  fennecPlan(M{"click": `textbox "Name"`}, M{"type": path}),
		Shuttle: shuttlePlan(M{"click": "Name"}, M{"type": path}),
		Keys:    []string{"controls.name", "focus"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.name", r.State.Str("controls.name"), path)
			c.that(r.Outcome.OK, "the plan failed: %s", r.Outcome.Detail)
			return c.err()
		},
	})
}

// CAP-STP-013: a dialog that shows late and fills later still gets its
// steps.
func TestSlowDialogFill(t *testing.T) {
	run(t, Case{
		Caps:  []string{"CAP-STP-013"},
		Scene: "slow",
		Fennec: fennecPlan(
			M{"press": `button "Open Slowly"`},
			M{"set": `textbox "Code"`, "value": "4711"},
			M{"press": `button "Confirm"`},
		),
		Shuttle: shuttlePlan(
			M{"click": "Open Slowly"},
			M{"set": "Code", "value": "4711"},
			M{"click": "Confirm"},
		),
		Keys: []string{"facts.confirmed_code"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("facts.confirmed_code", r.State.Str("facts.confirmed_code"), "4711")
			c.that(r.Outcome.OK, "the plan failed: %s", r.Outcome.Detail)
			return c.err()
		},
	})
}

var _ = time.Second
