//go:build scenario

package scenario

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/parity"
	"fennec/cua/internal/protocol"
)

// pt is a desktop point target.
func pt(x, y int) M { return M{"point": M{"x": x, "y": y}} }

// mustOK fails a check when the outcome did not pass.
func (c *checks) ok(r *Result) {
	c.that(r.Outcome.OK, "the plan failed: %s %s", r.Outcome.Class, r.Outcome.Detail)
}

// stopped expects a failed outcome.
func (c *checks) stopped(r *Result) {
	c.that(!r.Outcome.OK, "the plan passed but should have stopped")
}

// CAP-STP-006: a click that opens a popup list leaves the next step in the
// list.
func TestPopupListNextStep(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-STP-006"},
		Scene:   "form",
		Fennec:  fennecPlan(M{"click": `combobox "Color"`}, M{"press": `menuitem "Blue"`}),
		Shuttle: shuttlePlan(M{"click": "Color"}, M{"click": "Blue"}),
		Keys:    []string{"controls.color"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.color", r.State.Str("controls.color"), "Blue")
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-STP-014: a Return whose validation alert opens late is reported at
// the end of the plan, and the next call's step goes to it.
func TestLateDialogReportedAtEnd(t *testing.T) {
	run(t, Case{
		Caps:  []string{"CAP-STP-014"},
		Scene: "slow",
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			a, err := s.Steps(ctx, M{"click": `textbox "Query"`}, M{"type": "abc"}, M{"key": "return"})
			if err != nil {
				return parity.Outcome{}, err
			}
			reported := strings.Contains(fmt.Sprint(a.Res.Steps), "Validation Error") || (a.Res.Observe != nil && a.Res.Observe.Window != nil && a.Res.Observe.Window.Title == "Validation Error")
			if !reported && a.Res.Error != nil && a.Res.Error.Observe != nil && a.Res.Error.Observe.Window != nil {
				reported = a.Res.Error.Observe.Window.Title == "Validation Error"
			}
			b, err := s.Steps(ctx, M{"press": `button "OK"`})
			if err != nil {
				return parity.Outcome{}, err
			}
			o := b.Outcome()
			if !reported {
				o.OK, o.Class, o.Detail = false, "not_reported", "the first plan did not report the late Validation Error"
			}
			return o, nil
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			p, err := s.Plan(ctx, M{"click": "Query"}, M{"type": "abc"}, M{"key": "return"})
			if err != nil {
				return parity.Outcome{}, err
			}
			reported := strings.Contains(p.Raw, "Validation Error")
			q, err := s.Plan(ctx, M{"click": "OK"})
			if err != nil {
				return parity.Outcome{}, err
			}
			o := q.Outcome()
			if !reported {
				o.OK, o.Class, o.Detail = false, "not_reported", "the first plan did not report the late Validation Error"
			}
			return o, nil
		},
		Keys: []string{"facts.validation_shown", "controls.query"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.that(r.State.WindowTitled("Validation Error") == nil, "the Validation Error alert is still open")
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-STP-015: a typed path after a shortcut that opens the save panel late
// is saved once.
func TestTypePathIntoSavePanel(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-STP-015"},
		Scene:   "dialogs",
		Fennec:  fennecPlan(M{"key": "shift+cmd+s"}, M{"type": "report-1.txt\n"}),
		Shuttle: shuttlePlan(M{"key": "shift+cmd+s"}, M{"type": "report-1.txt\n"}),
		Keys:    []string{"facts.save_result", "facts.save_name"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("facts.save_result", r.State.Str("facts.save_result"), "saved")
			c.eq("facts.save_name", r.State.Str("facts.save_name"), "report-1.txt")
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGA-029: a Return that raises the replace confirmation is not
// repeated into it.
func TestSaveOverwriteConfirmNoSecondReturn(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGA-029"},
		Scene:   "dialogs",
		Fennec:  fennecPlan(M{"key": "shift+cmd+s"}, M{"type": "taken.txt\n"}),
		Shuttle: shuttlePlan(M{"key": "shift+cmd+s"}, M{"type": "taken.txt\n"}),
		Keys:    []string{"facts.save_result"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.that(r.State.Str("facts.save_result") != "saved", "the file was replaced: a second Return reached the confirmation")
			c.stopped(r)
			return c.err()
		},
	})
}

// CAP-RGA-040: a close step fails when its sheet stays open.
func TestCloseDialogStaysOpen(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGA-040"},
		Scene:   "dialogs",
		Fennec:  fennecPlan(M{"press": `button "Show Sheet"`}, M{"close": "Rename"}),
		Shuttle: shuttlePlan(M{"click": "Show Sheet"}, M{"close": true}),
		Keys:    []string{"facts.sheet_result"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.that(r.State.WindowTitled("Rename") != nil, "the Rename sheet closed")
			c.stopped(r)
			return c.err()
		},
	})
}

// CAP-RGB-040: a wait meets an alert in front and stops.
func TestWaitAlertStops(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-040"},
		Scene:   "dialogs",
		Fennec:  fennecPlan(M{"press": `button "Show Alert"`}, M{"wait": M{"front": "Dialogs", "timeout_ms": 1500}}),
		Shuttle: shuttlePlan(M{"click": "Show Alert"}, M{"wait": "Dialogs", "timeout_ms": 1500}),
		Keys:    []string{"facts.alert_result", "modal"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.that(r.State.Get("modal") != nil, "no app-modal alert is open")
			c.that(r.State.Get("facts.alert_result") == nil, "the alert was answered: %v", r.State.Get("facts.alert_result"))
			c.stopped(r)
			return c.err()
		},
	})
}

// CAP-RGB-053: a press that opens a modal alert counts once and is never
// followed by a click.
func TestPressOpensModalNoDoubleClick(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-053"},
		Scene:   "dialogs",
		Fennec:  fennecPlan(M{"press": `button "Show Alert"`}),
		Shuttle: shuttlePlan(M{"click": "Show Alert"}),
		Keys:    []string{"presses.show-alert", "modal"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("presses.show-alert", r.State.Num("presses.show-alert"), 1)
			c.that(r.State.Get("modal") != nil, "no app-modal alert is open")
			return c.err()
		},
	})
}

// CAP-RGB-041: elements whose press action is refused get one hit-checked
// click.
func TestPressRefusedFallsBackVerified(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-041"},
		Scene:   "canvas",
		Fennec:  chain(fennecPlan(M{"click": `button "Stubborn"`}), fennecPlan(M{"click": `"Logo Tile"`})),
		Shuttle: chain(shuttlePlan(M{"click": "Stubborn"}), shuttlePlan(M{"click": "Logo Tile"})),
		Keys:    []string{"controls.logo-tile-clicks", "presses.stubborn"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			// Stubborn refuses its press inside the app while AX reports
			// success, so no backend can see the refusal: it is recorded,
			// not judged.
			_ = r.State.Num("presses.stubborn")
			// The tile is clicked once, or, where the driver's snapshot
			// lacks it, refused with nothing clicked: never a pixel click.
			clicks := r.State.Num("controls.logo-tile-clicks")
			if r.Outcome.OK {
				c.eq("controls.logo-tile-clicks", clicks, 1)
			} else {
				c.eq("controls.logo-tile-clicks", clicks, 0)
				c.eq("refusal", r.Outcome.Class, string(protocol.ErrHitMismatch))
			}
			return c.err()
		},
	})
}

// CAP-RGA-017: a shortcut of a disabled control is refused.
func TestKeyEquivalentDisabledControl(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGA-017"},
		Scene:   "keys",
		Fennec:  fennecPlan(M{"key": "cmd+d"}),
		Shuttle: shuttlePlan(M{"key": "cmd+d"}),
		Keys:    []string{"presses.disabled-shortcut"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("presses.disabled-shortcut", r.State.Num("presses.disabled-shortcut"), 0)
			c.stopped(r)
			return c.err()
		},
	})
}

// keyChars lists the characters of the recorded key events.
func keyChars(st State) string {
	var b strings.Builder
	for _, k := range st.Events("keys") {
		b.WriteString(fmt.Sprint(k["chars"]))
	}
	return b.String()
}

// CAP-RGA-050: a key naming only an app reaches that app's window.
func TestKeyByAppOnly(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGA-050"},
		Scene:   "keys",
		Setup:   frontFinder,
		Fennec:  fennecPlan(M{"key": "x", "app": "CUAFixture"}),
		Shuttle: shuttleDesk("key", M{"keys": "x", "app": "CUAFixture"}),
		Keys:    []string{"events.keys"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.that(strings.Contains(keyChars(r.State), "x"), "the fixture got no x: %v", r.State.Get("events.keys"))
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-070: input naming an app without a window sends nothing, under
// auto input too.
func TestAppTargetNoWindowRefused(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-070"},
		Scene:   "empty",
		Config:  Config{Input: "auto"},
		Fennec:  fennecPlan(M{"key": "x", "app": "CUAFixture"}),
		Shuttle: shuttleDesk("key", M{"keys": "x", "app": "CUAFixture"}),
		Keys:    []string{"events.keys"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("key events", len(r.State.Events("keys")), 0)
			c.stopped(r)
			return c.err()
		},
	})
}

// CAP-RGA-001: a type with a focus check waits for focus that arrives
// 700 ms after the page shows.
func TestTypeFocusArrivesLate(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGA-001"},
		Scene:   "login",
		Fennec:  fennecPlan(M{"type": "alice", "into": `textbox "Username"`}),
		Shuttle: shuttlePlan(M{"type": "alice", "focus": M{"role": "textbox", "name": "Username"}}),
		Keys:    []string{"controls.username"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.username", r.State.Str("controls.username"), "alice")
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGA-001: a sheet that holds the focus stops a focus-checked type.
func TestTypeFocusHeldBySheet(t *testing.T) {
	signIn := func(ctx context.Context, s *Session, fennec bool) error {
		if fennec {
			_, err := s.Steps(ctx, M{"set": `textbox "Username"`, "value": "alice"}, M{"set": `textbox "Password"`, "value": "pw-1", "secret": true}, M{"press": `button "Sign In"`}, M{"wait": M{"window_opens": "Save Password?", "timeout_ms": 3000}})
			return err
		}
		_, err := s.Plan(ctx, M{"set": "Username", "value": "alice"}, M{"set": "Password", "value": "pw-1"}, M{"click": "Sign In"}, M{"wait": "Save Password?"})
		return err
	}
	run(t, Case{
		Caps:  []string{"CAP-RGA-001"},
		Scene: "login",
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			if err := signIn(ctx, s, true); err != nil {
				return parity.Outcome{}, err
			}
			a, err := s.Steps(ctx, M{"type": "bob", "into": `textbox "Username"`})
			if err != nil {
				return parity.Outcome{}, err
			}
			return a.Outcome(), nil
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			if err := signIn(ctx, s, false); err != nil {
				return parity.Outcome{}, err
			}
			p, err := s.Plan(ctx, M{"type": "bob", "focus": M{"role": "textbox", "name": "Username"}})
			if err != nil {
				return parity.Outcome{}, err
			}
			return p.Outcome(), nil
		},
		Keys: []string{"controls.username", "facts.signed_in"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("facts.signed_in", r.State.Num("facts.signed_in"), 1)
			c.eq("controls.username", r.State.Str("controls.username"), "alice")
			c.stopped(r)
			return c.err()
		},
	})
}

// CAP-RGB-076: a type whose required focus is elsewhere stops with nothing
// typed.
func TestTypeFocusMismatchStops(t *testing.T) {
	run(t, Case{
		Caps:  []string{"CAP-RGB-076"},
		Scene: "login",
		Setup: func(ctx context.Context) error { return sleep(ctx, 1000) },
		// The focus moved into Username; the step wants the password
		// field.
		Fennec:  fennecPlan(M{"type": "pw-2", "secret": true, "into": `textbox "Password" protected`}),
		Shuttle: shuttlePlan(M{"type": "pw-2", "secret": true, "focus": M{"secure": true}}),
		Keys:    []string{"controls.username", "controls.password"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.username", r.State.Str("controls.username"), "")
			c.eq("controls.password", r.State.Str("controls.password"), "")
			c.stopped(r)
			for _, t := range r.Texts {
				c.that(!strings.Contains(resultText(t), "pw-2"), "the password shows in a result")
			}
			return c.err()
		},
	})
}

// CAP-RGB-038: a click on a text field focuses it for the typing after.
func TestTextFieldClickFocuses(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-038"},
		Scene:   "form",
		Fennec:  fennecPlan(M{"click": `textbox "Email:"`}, M{"type": "a@b.example"}),
		Shuttle: shuttlePlan(M{"click": "Email:"}, M{"type": "a@b.example"}),
		Keys:    []string{"controls.email", "controls.name"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.email", r.State.Str("controls.email"), "a@b.example")
			c.eq("controls.name", r.State.Str("controls.name"), "")
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGA-039: a field with a unit and a slider are set to equal values.
func TestSliderSetWithUnit(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGA-039"},
		Scene:   "form",
		Fennec:  fennecPlan(M{"set": `spinbutton "Font Size Stepper"`, "value": 14}, M{"set": `slider "Volume"`, "value": 30}),
		Shuttle: shuttlePlan(M{"set": "Font Size Stepper", "value": "14"}, M{"set": "Volume", "value": "30"}),
		Keys:    []string{"controls.font-size", "controls.volume"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.font-size", r.State.Num("controls.font-size"), 14)
			c.eq("controls.volume", r.State.Num("controls.volume"), 30)
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-011: wheel notches at a point scroll the list under it.
func TestScrollAtPoint(t *testing.T) {
	var x, y int
	setup := func(ctx context.Context) error {
		b, err := boundsOf(ctx, "Scroll", "scrollarea", "Long List")
		if err != nil {
			return err
		}
		x, y = center(b)
		return nil
	}
	run(t, Case{
		Caps:  []string{"CAP-RGB-011"},
		Scene: "scroll",
		Setup: setup,
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return fennecPlan(M{"scroll": pt(x, y), "direction": "down", "amount": 5})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return shuttleDesk("scroll", M{"x": x, "y": y, "direction": "down", "amount": 5})(ctx, s)
		},
		Keys: []string{},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.that(r.State.Num("controls.long-list-scroll-y") > 0, "Long List did not scroll: %v", r.State.Get("controls.long-list-scroll-y"))
			c.eq("controls.long-text-scroll-y", r.State.Num("controls.long-text-scroll-y"), 0)
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-011: both ends of a drag pass the allowlist.
func TestDragBothEndsAllowlisted(t *testing.T) {
	var tx, ty, zx, zy, ox, oy int
	setup := func(ctx context.Context) error {
		tok, err := boundsOf(ctx, "Drag", "", "Token")
		if err != nil {
			return err
		}
		zone, err := boundsOf(ctx, "Drag", "", "Drop Zone")
		if err != nil {
			return err
		}
		tx, ty = center(tok)
		zx, zy = center(zone)
		// A point on the desktop right of every window: Finder's.
		w, err := window(ctx, "CUAFixture", "Drag")
		if err != nil {
			return err
		}
		ox, oy = w.Bounds[0]+w.Bounds[2]+300, w.Bounds[1]+100
		return nil
	}
	run(t, Case{
		Caps:   []string{"CAP-RGB-011", "CAP-DSK-030"},
		Scene:  "drag",
		Config: Config{Allow: []string{"CUAFixture"}},
		Setup:  setup,
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			a, err := s.Steps(ctx, M{"drag": pt(tx, ty), "to": pt(ox, oy)})
			if err != nil {
				return parity.Outcome{}, err
			}
			if a.OK() || a.Code() != string(protocol.ErrNotAllowed) {
				return parity.Outcome{Class: "allowed", Detail: "a drag onto Finder's desktop was not refused by the allowlist: " + a.Code()}, nil
			}
			return fennecPlan(M{"drag": pt(tx, ty), "to": pt(zx, zy)})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			d, err := s.Desk(ctx, "drag", M{"x": tx, "y": ty, "to_x": ox, "to_y": oy})
			if err != nil {
				return parity.Outcome{}, err
			}
			if !d.IsError {
				return parity.Outcome{Class: "allowed", Detail: "a drag onto Finder's desktop was not refused by the allowlist"}, nil
			}
			return shuttleDesk("drag", M{"x": tx, "y": ty, "to_x": zx, "to_y": zy})(ctx, s)
		},
		Keys: []string{"controls.token-in-zone"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.token-in-zone", r.State.Bool("controls.token-in-zone"), true)
			c.eq("drags", len(r.State.Events("drags")), 1)
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-018: launching a running app behind Finder fronts its window.
func TestLaunchFrontsWindow(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-018"},
		Scene:   "launch",
		Setup:   frontFinder,
		Fennec:  fennecPlan(M{"launch": "CUAFixture"}),
		Shuttle: shuttlePlan(M{"app": "CUAFixture"}),
		Keys:    []string{"active"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("active", r.State.Bool("active"), true)
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-018: two launches in a row still end with the window in front.
func TestFastRelaunchFronts(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-018"},
		Scene:   "launch",
		Setup:   frontFinder,
		Fennec:  chain(fennecPlan(M{"launch": "CUAFixture"}), fennecPlan(M{"launch": "CUAFixture"})),
		Shuttle: chain(shuttlePlan(M{"app": "CUAFixture"}), shuttlePlan(M{"app": "CUAFixture"})),
		Keys:    []string{"active"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("active", r.State.Bool("active"), true)
			c.eq("instances", r.State.Num("launch.instances"), 1)
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-STP-032: a launch with a URL hands it to the running instance.
func TestOpenURLInBrowser(t *testing.T) {
	const u = "cua-fixture://open/one"
	run(t, Case{
		Caps:    []string{"CAP-STP-032", "CAP-RGA-014"},
		Scene:   "launch",
		Fennec:  fennecPlan(M{"launch": "CUAFixture", "url": u}),
		Shuttle: shuttlePlan(M{"app": "CUAFixture", "url": u}),
		Keys:    []string{"launch.opened"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.that(strings.Contains(fmt.Sprint(r.State.Get("launch.opened")), u), "the URL did not reach the app: %v", r.State.Get("launch.opened"))
			c.eq("instances", r.State.Num("launch.instances"), 1)
			return c.err()
		},
	})
}

// CAP-RGA-057: a link's node carries its exact URL.
func TestWebLinksCarryURL(t *testing.T) {
	const want = "https://example.com/Docs/Fennec?ref=CUA"
	run(t, Case{
		Caps:  []string{"CAP-RGA-057"},
		Scene: "link",
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			// An element read always carries a link's target.
			o, perr, err := s.Observe(ctx, M{"scope": "element", "target": `link "Fennec Docs"`})
			if err != nil {
				return parity.Outcome{}, err
			}
			if perr != nil {
				return parity.Outcome{Class: string(perr.Code), Detail: perr.Message}, nil
			}
			if o.Element != nil && o.Element.URL == want {
				return parity.Outcome{OK: true}, nil
			}
			return parity.Outcome{Class: "url_missing", Detail: fmt.Sprintf("%+v", o.Element)}, nil
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			d, err := s.Desk(ctx, "find", M{"name": "Fennec Docs", "urls": true})
			if err != nil {
				return parity.Outcome{}, err
			}
			if strings.Contains(d.Text, want) {
				return parity.Outcome{OK: true}, nil
			}
			return parity.Outcome{Class: "url_missing", Detail: clip(d.Text, 300)}, nil
		},
		Keys: []string{"events.links"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGA-058: a hung app's window stays listed as stale, and listing does
// not wait for it.
func TestHungAppStaleWindow(t *testing.T) {
	stale := func(text string) bool { return strings.Contains(text, `"stale":true`) }
	run(t, Case{
		Caps:    []string{"CAP-RGA-058"},
		Scene:   "busy",
		Timeout: 3 * time.Minute,
		Setup: func(ctx context.Context) error {
			// List once while the app answers, then freeze it.
			if _, err := windows(ctx); err != nil {
				return err
			}
			return nil
		},
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			if _, _, err := s.Observe(ctx, M{"scope": "desktop"}); err != nil {
				return parity.Outcome{}, err
			}
			if err := h.fx.Freeze(ctx, 8*time.Second); err != nil {
				return parity.Outcome{}, err
			}
			if err := sleep(ctx, 1500); err != nil {
				return parity.Outcome{}, err
			}
			start := time.Now()
			r, err := s.Tool(ctx, protocol.ToolObserve, M{"scope": "desktop"})
			if err != nil {
				return parity.Outcome{}, err
			}
			return staleOutcome(stale(r.Text()) && strings.Contains(r.Text(), "Busy"), time.Since(start), r.Text()), nil
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			if _, err := s.Desk(ctx, "windows", nil); err != nil {
				return parity.Outcome{}, err
			}
			if err := h.fx.Freeze(ctx, 8*time.Second); err != nil {
				return parity.Outcome{}, err
			}
			if err := sleep(ctx, 1500); err != nil {
				return parity.Outcome{}, err
			}
			start := time.Now()
			d, err := s.Desk(ctx, "windows", nil)
			if err != nil {
				return parity.Outcome{}, err
			}
			return staleOutcome(stale(d.Text) && strings.Contains(d.Text, "Busy"), time.Since(start), d.Text), nil
		},
		Keys: []string{"freezes"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	})
}

func staleOutcome(ok bool, took time.Duration, text string) parity.Outcome {
	switch {
	case !ok:
		return parity.Outcome{Class: "not_stale", Detail: clip(text, 400)}
	case took > 3*time.Second:
		return parity.Outcome{Class: "slow", Detail: fmt.Sprintf("the listing waited %s for the hung app", took)}
	}
	return parity.Outcome{OK: true, Detail: fmt.Sprintf("listed in %s", took)}
}

// longTitle finds the fixture window whose title the window server elides.
func longTitle(ctx context.Context) (string, [4]int, error) {
	ws, err := windows(ctx)
	if err != nil {
		return "", [4]int{}, err
	}
	for _, w := range ws {
		if w.App == "CUAFixture" && len(w.Title) > 100 && w.Bounds != nil {
			return w.Title, *w.Bounds, nil
		}
	}
	return "", [4]int{}, errors.New("no long-titled fixture window")
}

// CAP-RGA-063: a hit test inside a window with an elided long title names
// the window by its whole title.
func TestHitTestLongTitle(t *testing.T) {
	var title string
	var x, y int
	setup := func(ctx context.Context) error {
		var b [4]int
		var err error
		title, b, err = longTitle(ctx)
		x, y = center(b)
		return err
	}
	run(t, Case{
		Caps:  []string{"CAP-RGA-063"},
		Scene: "windows",
		Setup: setup,
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			o, perr, err := s.Observe(ctx, M{"scope": "point", "target": pt(x, y)})
			if err != nil {
				return parity.Outcome{}, err
			}
			if perr != nil {
				return parity.Outcome{Class: string(perr.Code), Detail: perr.Message}, nil
			}
			if o.At == nil || o.At.Window == nil || o.At.Window.Title != title {
				return parity.Outcome{Class: "wrong_window", Detail: fmt.Sprintf("%+v", o.At)}, nil
			}
			return parity.Outcome{OK: true}, nil
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			d, err := s.Desk(ctx, "at", M{"x": x, "y": y})
			if err != nil {
				return parity.Outcome{}, err
			}
			if d.IsError || !strings.Contains(d.Text, title) {
				return parity.Outcome{Class: "wrong_window", Detail: clip(d.Text, 300)}, nil
			}
			return parity.Outcome{OK: true}, nil
		},
		Keys: []string{},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	})
}

const formURL = "https://fixture.example/app/form"

// webClicks reads the web scene's click count of a DOM id.
func webClicks(st State, id string) float64 { return st.Num("controls.web.clicks." + id) }

// CAP-RGB-083: a web step prefers the element with the recorded DOM id.
func TestWebStepDomIdPreferred(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-083"},
		Scene:   "web",
		Fennec:  fennecPlan(M{"press": `button "Save"`, "web": M{"url": formURL, "dom_id": "save-bottom"}}),
		Shuttle: shuttlePlan(M{"click": "Save", "web": M{"url": formURL, "id": "save-bottom"}}),
		Keys:    []string{"controls.web.clicks"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("save-bottom clicks", webClicks(r.State, "save-bottom"), 1)
			c.eq("save-top clicks", webClicks(r.State, "save-top"), 0)
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-STP-029: a redirect to another path stops a web step before it
// clicks.
func TestWebRedirectStops(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-STP-029"},
		Scene:   "web",
		Fennec:  fennecPlan(M{"press": `button "Go to Login"`}, M{"press": `button "Save"`, "web": M{"url": formURL}}),
		Shuttle: shuttlePlan(M{"click": "Go to Login"}, M{"click": "Save", "web": M{"url": formURL}}),
		Keys:    []string{"controls.web.path", "controls.web.clicks"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.web.path", r.State.Str("controls.web.path"), "/app/login")
			c.eq("save clicks", webClicks(r.State, "save-top")+webClicks(r.State, "save-bottom"), 0)
			c.stopped(r)
			return c.err()
		},
	})
}

// CAP-RGB-083: a web step on another page than its own clicks nothing.
func TestWebStepPageMismatchStops(t *testing.T) {
	const other = "https://fixture.example/app/other"
	run(t, Case{
		Caps:    []string{"CAP-RGB-083"},
		Scene:   "web",
		Fennec:  fennecPlan(M{"press": `button "Save"`, "web": M{"url": other}}),
		Shuttle: shuttlePlan(M{"click": "Save", "web": M{"url": other}}),
		Keys:    []string{"controls.web.clicks"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("save clicks", webClicks(r.State, "save-top")+webClicks(r.State, "save-bottom"), 0)
			c.stopped(r)
			return c.err()
		},
	})
}
