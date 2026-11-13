//go:build scenario

package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os/exec"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/daemon"
	"fennec/cua/internal/parity"
	"fennec/cua/internal/protocol"
)

// ---------------------------------------------------------------------------
// Background and auto input

// CAP-RGB-023: background typing reaches the named window and leaves the
// front app in front.
func TestBackgroundTypeNoFocusSteal(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-023"},
		Scene:   "basic",
		Config:  Config{Input: "background"},
		Setup:   frontFinder,
		Fennec:  fennecPlan(M{"click": `textbox "Note"`, "window": "Basic"}, M{"type": "hello", "window": "Basic"}),
		Shuttle: shuttlePlan(M{"app": "CUAFixture", "title": "Basic"}, M{"click": "Note"}, M{"type": "hello"}),
		Keys:    []string{"controls.basic-note", "active"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.basic-note", r.State.Str("controls.basic-note"), "hello")
			c.eq("active", r.State.Bool("active"), false)
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-027: a background plan drives a window's save sheet without
// fronting it.
func TestBackgroundPlanSaveSheet(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-027"},
		Scene:   "dialogs",
		Config:  Config{Input: "background"},
		Setup:   frontFinder,
		Fennec:  fennecPlan(M{"press": "button \"Save As…\"", "window": "Dialogs"}, M{"type": "bg-report.txt\n", "window": "Dialogs"}),
		Shuttle: shuttlePlan(M{"app": "CUAFixture", "title": "Dialogs"}, M{"click": "Save As…"}, M{"type": "bg-report.txt\n"}),
		Keys:    []string{"facts.save_result", "facts.save_name", "active"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("facts.save_result", r.State.Str("facts.save_result"), "saved")
			c.eq("facts.save_name", r.State.Str("facts.save_name"), "bg-report.txt")
			c.eq("active", r.State.Bool("active"), false)
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-027: a background plan refuses a title two windows share.
func TestBackgroundPlanRefusesTwinTitles(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-027"},
		Scene:   "twins",
		Config:  Config{Input: "background"},
		Fennec:  fennecPlan(M{"press": `button "Mark"`, "window": "Twin"}),
		Shuttle: shuttlePlan(M{"app": "CUAFixture", "title": "Twin"}, M{"click": "Mark"}),
		Keys:    []string{"presses"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			for _, w := range []string{"twin-a", "twin-b", "twin-c"} {
				c.eq("presses.mark@"+w, r.State.Num("presses.mark@"+w), 0)
			}
			c.stopped(r)
			return c.err()
		},
	})
}

// CAP-RGB-045: an app's only window of a title is picked though minimized.
func TestHiddenTwinWindow(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-045"},
		Scene:   "twins",
		Config:  Config{Input: "background"},
		Fennec:  fennecPlan(M{"set": `textbox "Note"`, "value": "solo", "window": "Solo"}),
		Shuttle: shuttlePlan(M{"app": "CUAFixture", "title": "Solo"}, M{"set": "Note", "value": "solo"}),
		Keys:    []string{"controls"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.note@solo", r.State.Str("controls.note@solo"), "solo")
			for _, w := range []string{"twin-a", "twin-b", "twin-c"} {
				c.eq("controls.note@"+w, r.State.Str("controls.note@"+w), "")
			}
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-032: under auto input a menu step, which background input
// cannot do, runs in the foreground.
func TestAutoFallsBackOnRefusal(t *testing.T) {
	run(t, Case{
		Caps:   []string{"CAP-RGB-032"},
		Scene:  "menus",
		Config: Config{Input: "auto"},
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			a, err := s.Steps(ctx, M{"menu": "View > Show Status Bar", "value": true})
			if err != nil {
				return parity.Outcome{}, err
			}
			o := a.Outcome()
			if a.OK() && a.Step(0).Input != protocol.InputForeground {
				o.OK, o.Class, o.Detail = false, "input", "the step does not say it took the foreground: "+a.Step(0).Input
			}
			return o, nil
		},
		Shuttle: shuttlePlan(M{"menu": "View > Show Status Bar", "on": true}),
		Keys:    []string{"menu.status_bar"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("menu.status_bar", r.State.Bool("menu.status_bar"), true)
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGA-043: after an auto plan took the foreground, its later key and
// type steps are delivered there too.
func TestAutoModeForegroundFollowup(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGA-043"},
		Scene:   "menus",
		Config:  Config{Input: "auto"},
		Fennec:  fennecPlan(M{"menu": "View > Show Status Bar", "value": true}, M{"key": "cmd+a"}, M{"type": "Replaced"}),
		Shuttle: shuttlePlan(M{"menu": "View > Show Status Bar", "on": true}, M{"key": "cmd+a"}, M{"type": "Replaced"}),
		Keys:    []string{"menu.status_bar", "controls.menus-body"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("menu.status_bar", r.State.Bool("menu.status_bar"), true)
			c.that(r.State.Num("edits.menus-body") > 0, "the body was never edited: %v", r.State.Get("edits"))
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGA-031: under auto input a password is typed once, never retyped.
func TestAutoModePasswordNoRetype(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGA-031"},
		Scene:   "login",
		Config:  Config{Input: "auto"},
		Setup:   func(ctx context.Context) error { return sleep(ctx, 1000) },
		Fennec:  fennecPlan(M{"click": `textbox "Password"`, "window": "Sign In"}, M{"type": "pw-3", "secret": true, "window": "Sign In"}),
		Shuttle: shuttlePlan(M{"app": "CUAFixture", "title": "Sign In"}, M{"click": "Password"}, M{"type": "pw-3", "focus": M{"secure": true}}),
		Keys:    []string{"controls.password"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			p := r.State.Str("controls.password")
			c.that(p == "pw-3" || p == "", "the password field holds %d characters, not 4 or none", len(p))
			c.that(strings.Count(p, "pw-3") <= 1, "the password was typed twice")
			c.noSecret(r, "pw-3")
			return c.err()
		},
	})
}

// CAP-RGA-019: a background line into a single-line field gets its Return
// as a key of its own.
func TestBackgroundTypeReturnSingleLine(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGA-019"},
		Scene:   "slow",
		Config:  Config{Input: "background"},
		Setup:   frontFinder,
		Fennec:  fennecPlan(M{"click": `textbox "Query"`, "window": "Slow Start"}, M{"type": "abc\n", "window": "Slow Start"}),
		Shuttle: shuttlePlan(M{"app": "CUAFixture", "title": "Slow Start"}, M{"click": "Query"}, M{"type": "abc\n"}),
		Keys:    []string{"controls.query", "facts.validation_shown"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.query", r.State.Str("controls.query"), "abc")
			if err := sleep(ctx, 1200); err == nil {
				st, _ := h.fx.RawState(ctx)
				c.that(State(st).WindowTitled("Validation Error") != nil, "the Return did not raise the Validation Error alert")
			}
			return c.err()
		},
	})
}

// CAP-RGB-059: a background type refuses when the last background click
// left the focus in a field that does not hold the click point.
func TestBackgroundTypeWrongFocusRefused(t *testing.T) {
	var fx, fy, ex, ey int
	setup := func(ctx context.Context) error {
		b, err := boundsOf(ctx, "Form", "textbox", "Name")
		if err != nil {
			return err
		}
		fx, fy = center(b)
		w, err := window(ctx, "CUAFixture", "Form")
		if err != nil {
			return err
		}
		// An empty spot right of the fields.
		ex, ey = w.Bounds[0]+w.Bounds[2]-30, fy
		return nil
	}
	run(t, Case{
		Caps:   []string{"CAP-RGB-059"},
		Scene:  "form",
		Config: Config{Input: "background"},
		Setup:  setup,
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return fennecPlan(M{"click": pt(fx, fy), "window": "Form"}, M{"click": pt(ex, ey), "window": "Form"}, M{"type": "zz", "window": "Form"})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return chain(
				shuttleDesk("click", M{"x": fx, "y": fy, "window": "Form"}),
				shuttleDesk("click", M{"x": ex, "y": ey, "window": "Form"}),
				shuttleDesk("type", M{"text": "zz", "window": "Form"}),
			)(ctx, s)
		},
		Keys: []string{"controls.name"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.name", r.State.Str("controls.name"), "")
			c.stopped(r)
			return c.err()
		},
	})
}

// CAP-RGB-035: background input with the driver's overlay on stays fast.
func TestBackgroundInputLatencyWithOverlay(t *testing.T) {
	run(t, Case{
		Caps:      []string{"CAP-RGB-035"},
		Scene:     "basic",
		Config:    Config{Input: "background"},
		NoShuttle: "spec capability: fennec-cua only",
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			if _, err := s.Steps(ctx, M{"click": `textbox "Note"`, "window": "Basic"}); err != nil {
				return parity.Outcome{}, err
			}
			start := time.Now()
			a, err := s.Steps(ctx, M{"type": "x", "window": "Basic"})
			if err != nil {
				return parity.Outcome{}, err
			}
			o := a.Outcome()
			took := time.Since(start)
			o.Detail = fmt.Sprintf("a background type took %s; %s", took, o.Detail)
			if took > 1500*time.Millisecond {
				o.OK, o.Class = false, "slow"
			}
			return o, nil
		},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			c.eq("controls.basic-note", r.State.Str("controls.basic-note"), "x")
			return c.err()
		},
	})
}

// CAP-RGB-030: foreground typing of plain text has no per-character delay.
func TestForegroundTypingPace(t *testing.T) {
	const text = "abcdefghijklmnopqrst"
	run(t, Case{
		Caps:      []string{"CAP-RGB-030"},
		Scene:     "basic",
		NoShuttle: "spec capability: fennec-cua only",
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			a, err := s.Steps(ctx, M{"click": `textbox "Note"`}, M{"type": text})
			if err != nil {
				return parity.Outcome{}, err
			}
			o := a.Outcome()
			ms := a.Step(1).Ms
			o.Detail = fmt.Sprintf("20 characters in %d ms; %s", ms, o.Detail)
			if ms > 1500 {
				o.OK, o.Class = false, "slow"
			}
			return o, nil
		},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.basic-note", r.State.Str("controls.basic-note"), text)
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-051: foreground input naming a window fronts it first.
func TestForegroundKeyFrontsNamedWindow(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-051"},
		Scene:   "windows",
		Fennec:  fennecPlan(M{"key": "q", "window": "Edit Me"}),
		Shuttle: shuttleDesk("key", M{"keys": "q", "window": "Edit Me"}),
		Keys:    []string{"windows"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.that(r.State.WindowTitled("Edit Me*") != nil, "Edit Me took no key: %v", r.State.Get("windows"))
			return c.err()
		},
	})
}

// ---------------------------------------------------------------------------
// The user's own input

// CAP-RGB-084: foreground input yields while the user moves the mouse and
// goes through once they stop.
func TestYieldToUserMouse(t *testing.T) {
	var x, y int
	setup := func(ctx context.Context) (err error) {
		b, err := boundsOf(ctx, "Basic", "button", "Increment")
		x, y = center(b)
		return err
	}
	drive := func(click func(context.Context, *Session) (parity.Outcome, error)) Driver {
		return func(ctx context.Context, s *Session) (parity.Outcome, error) {
			userMove(x+300, y+200)
			o, err := click(ctx, s)
			if err != nil {
				return o, err
			}
			if o.OK {
				return parity.Outcome{Class: "not_yielded", Detail: "a click went out while the user moved the mouse"}, nil
			}
			if err := waitIdle(ctx, 1100*time.Millisecond); err != nil {
				return parity.Outcome{}, err
			}
			return click(ctx, s)
		}
	}
	run(t, Case{
		Caps:  []string{"CAP-RGB-084"},
		Scene: "basic",
		Setup: setup,
		Fennec: drive(func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return fennecPlan(M{"click": pt(x, y)})(ctx, s)
		}),
		Shuttle: drive(func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return shuttleDesk("click", M{"x": x, "y": y})(ctx, s)
		}),
		Keys: []string{"presses.increment"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("presses.increment", r.State.Num("presses.increment"), 1)
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-084: fennec-cua's own typing is not the user's: the next
// foreground step goes at once.
func TestOwnTypingNotUser(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-084"},
		Scene:   "basic",
		Fennec:  chain(fennecPlan(M{"click": `textbox "Note"`}, M{"type": "abc"}), fennecPlan(M{"press": `button "Increment"`}), fennecPlan(M{"key": "tab"})),
		Shuttle: chain(shuttlePlan(M{"click": "Note"}, M{"type": "abc"}), shuttlePlan(M{"click": "Increment"}), shuttleDesk("key", M{"keys": "tab"})),
		Keys:    []string{"presses.increment", "controls.basic-note"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("controls.basic-note", r.State.Str("controls.basic-note"), "abc")
			c.eq("presses.increment", r.State.Num("presses.increment"), 1)
			c.ok(r)
			return c.err()
		},
	})
}

// ---------------------------------------------------------------------------
// Pixels

// pngOf decodes a PNG.
func pngOf(data []byte) (image.Image, error) { return png.Decode(bytes.NewReader(data)) }

// fennecWindowShot captures a window and maps a desktop point into it.
func fennecWindowShot(ctx context.Context, s *Session, title string) (*protocol.ObserveResult, image.Image, error) {
	o, data, perr, err := s.ObserveImage(ctx, M{"scope": "window", "window": title, "image": "window"})
	if err != nil {
		return nil, nil, err
	}
	if perr != nil {
		return nil, nil, perr
	}
	if o.Image == nil || data == nil {
		return nil, nil, fmt.Errorf("no window image")
	}
	img, err := pngOf(data)
	return o, img, err
}

// CAP-RGB-024: a click right after a stillness wait lands on its point.
func TestClickAfterStillLandsOnPoint(t *testing.T) {
	var x, y int
	run(t, Case{
		Caps:  []string{"CAP-RGB-024"},
		Scene: "click-pad",
		Setup: func(ctx context.Context) (err error) {
			x, y, err = padCell(ctx, "Click Pad", "Click Pad", "C2")
			return err
		},
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			if _, perr, err := s.Wait(ctx, M{"still": true, "timeout_ms": 2000}); err != nil || perr != nil {
				return parity.Outcome{Class: "wait"}, err
			}
			return fennecPlan(M{"click": pt(x, y)})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return chain(shuttleDesk("still", M{"timeout_ms": 2000}), shuttleDesk("click", M{"x": x, "y": y}))(ctx, s)
		},
		Keys:  []string{},
		Check: lastCell("C2"),
	})
}

// lastCell checks that the pad got exactly one click, in cell.
func lastCell(cell string) func(context.Context, *Result) error {
	return func(ctx context.Context, r *Result) error {
		var c checks
		cl := r.State.Events("clicks")
		c.eq("clicks", len(cl), 1)
		if len(cl) > 0 {
			c.eq("cell", cl[len(cl)-1]["cell"], cell)
		}
		return c.err()
	}
}

// CAP-RGB-049: a click in window-image pixels lands where the image shows.
func TestClickInWindowImagePixels(t *testing.T) {
	clickPixels(t, "CAP-RGB-049", false)
}

// CAP-RGB-049: a window wider than 1280 image pixels keeps its image's
// scale for the click after its capture.
func TestClickAfterWideWindowShot(t *testing.T) {
	clickPixels(t, "CAP-RGB-049", true)
}

func clickPixels(t *testing.T, cap string, wide bool) {
	var x, y int
	setup := func(ctx context.Context) error {
		if wide {
			w, err := window(ctx, "CUAFixture", "Click Pad")
			if err != nil {
				return err
			}
			if err := resizeWindow(w.PID, "Click Pad", 980, 420); err != nil {
				return err
			}
			if err := sleep(ctx, 400); err != nil {
				return err
			}
		}
		var err error
		x, y, err = padCell(ctx, "Click Pad", "Click Pad", "D3")
		return err
	}
	run(t, Case{
		Caps:  []string{cap},
		Scene: "click-pad",
		Setup: setup,
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			o, _, err := fennecWindowShot(ctx, s, "Click Pad")
			if err != nil {
				return parity.Outcome{Class: "capture", Detail: err.Error()}, nil
			}
			im := o.Image
			px := (float64(x) - im.Origin[0]) / im.Scale
			py := (float64(y) - im.Origin[1]) / im.Scale
			return fennecPlan(M{"click": M{"point": M{"x": px, "y": py, "space": "window", "view": o.View}}, "window": "Click Pad"})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			r, err := s.Tool(ctx, "screenshot", M{"window": "Click Pad"})
			if err != nil {
				return parity.Outcome{}, err
			}
			data, _ := firstImage(r)
			if data == nil {
				return parity.Outcome{Class: "capture", Detail: clip(r.Text(), 200)}, nil
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				return parity.Outcome{Class: "capture", Detail: err.Error()}, nil
			}
			w, err := window(ctx, "CUAFixture", "Click Pad")
			if err != nil {
				return parity.Outcome{}, err
			}
			sc := float64(cfg.Width) / float64(w.Bounds[2])
			px, py := int(float64(x-w.Bounds[0])*sc), int(float64(y-w.Bounds[1])*sc)
			return shuttleDesk("click", M{"x": px, "y": py, "window": "Click Pad"})(ctx, s)
		},
		Keys:  []string{},
		Check: lastCell("D3"),
	})
}

// CAP-RGB-022: a window image never shows the pointer: the area under the
// pointer looks the same with the pointer elsewhere.
func TestWindowShotNoPointer(t *testing.T) {
	var ax0, ay0, bx, by int
	setup := func(ctx context.Context) error {
		var err error
		if ax0, ay0, err = padCell(ctx, "Click Pad", "Click Pad", "B2"); err != nil {
			return err
		}
		bx, by, err = padCell(ctx, "Click Pad", "Click Pad", "D4")
		return err
	}
	run(t, Case{
		Caps:  []string{"CAP-RGB-022"},
		Scene: "click-pad",
		Setup: setup,
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			shot := func() (*protocol.ObserveResult, image.Image, error) { return fennecWindowShot(ctx, s, "Click Pad") }
			userMove(ax0, ay0)
			if err := sleep(ctx, 300); err != nil {
				return parity.Outcome{}, err
			}
			o1, i1, err := shot()
			if err != nil {
				return parity.Outcome{Class: "capture", Detail: err.Error()}, nil
			}
			userMove(bx, by)
			if err := sleep(ctx, 300); err != nil {
				return parity.Outcome{}, err
			}
			_, i2, err := shot()
			if err != nil {
				return parity.Outcome{Class: "capture", Detail: err.Error()}, nil
			}
			px := int((float64(ax0) - o1.Image.Origin[0]) / o1.Image.Scale)
			py := int((float64(ay0) - o1.Image.Origin[1]) / o1.Image.Scale)
			return sameArea(i1, i2, px, py, 24), nil
		},
		NoShuttle: "shuttle's screenshot tool is compared in TestClickInWindowImagePixels; the pointer check needs the harness's own pointer moves on fennec-cua's capture path",
		Keys:      []string{},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	})
}

// sameArea compares a square of two images around a point.
func sameArea(a, b image.Image, x, y, half int) parity.Outcome {
	diff := 0
	for yy := y - half; yy < y+half; yy++ {
		for xx := x - half; xx < x+half; xx++ {
			p := image.Point{xx, yy}
			if !p.In(a.Bounds()) || !p.In(b.Bounds()) {
				continue
			}
			r1, g1, b1, _ := a.At(xx, yy).RGBA()
			r2, g2, b2, _ := b.At(xx, yy).RGBA()
			if absDiff(r1, r2) > 24<<8 || absDiff(g1, g2) > 24<<8 || absDiff(b1, b2) > 24<<8 {
				diff++
			}
		}
	}
	if diff > 4 {
		return parity.Outcome{Class: "pointer_shown", Detail: fmt.Sprintf("%d pixels differ around the pointer", diff)}
	}
	return parity.Outcome{OK: true}
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

// CAP-RGB-055: a background click carries its in-window location, so the
// app sees the cell that was clicked.
func TestBackgroundClickWindowLocation(t *testing.T) {
	var x, y int
	run(t, Case{
		Caps:   []string{"CAP-RGB-055"},
		Scene:  "click-pad",
		Config: Config{Input: "background"},
		// A spec capability; shuttle runs too, for the record.
		Setup: func(ctx context.Context) (err error) {
			if err = frontFinder(ctx); err != nil {
				return err
			}
			x, y, err = padCell(ctx, "Click Pad", "Click Pad", "E1")
			return err
		},
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return fennecPlan(M{"click": pt(x, y), "window": "Click Pad"})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			w, err := window(ctx, "CUAFixture", "Click Pad")
			if err != nil {
				return parity.Outcome{}, err
			}
			return shuttleDesk("click", M{"x": x - w.Bounds[0], "y": y - w.Bounds[1], "window": "Click Pad"})(ctx, s)
		},
		Keys: []string{},
		Check: func(ctx context.Context, r *Result) error {
			// The click lands in its cell; where the driver cannot deliver a
			// background click to this window, the result must not claim it did.
			if len(r.State.Events("clicks")) == 0 && !r.Outcome.OK {
				return nil
			}
			return lastCell("E1")(ctx, r)
		},
	})
}

// CAP-RGB-028: a click on a vision screenshot's element lands on it.
func TestVisionIndexClick(t *testing.T) {
	run(t, Case{
		Caps:      []string{"CAP-RGB-028"},
		Scene:     "basic",
		NoShuttle: "shuttle's MCP desk tool does not offer the computer_* vision verbs (only script_run's desk API does)",
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			o, perr, err := s.Observe(ctx, M{"vision": true})
			if err != nil {
				return parity.Outcome{}, err
			}
			if perr != nil {
				return parity.Outcome{Class: string(perr.Code), Detail: perr.Message}, nil
			}
			for _, m := range o.Marks {
				if m.Name == "Increment" {
					return fennecPlan(M{"click": M{"point": M{"x": m.Center[0], "y": m.Center[1], "space": "image", "view": o.View}}})(ctx, s)
				}
			}
			return parity.Outcome{Class: "no_mark", Detail: fmt.Sprintf("%d marks, none Increment", len(o.Marks))}, nil
		},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("presses.increment", r.State.Num("presses.increment"), 1)
			c.ok(r)
			return c.err()
		},
	})
}

// ---------------------------------------------------------------------------
// Protocol and safety

// CAP-RGB-002: tools/list names the REPL's tools (js, js_reset); the REPL
// answers, and each engine tool it reaches answers by name too.
func TestEveryToolAnswers(t *testing.T) {
	run(t, Case{
		Caps:      []string{"CAP-RGB-002"},
		Scene:     "basic",
		NoShuttle: "spec capability: fennec-cua's own tool set",
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			names, err := s.c.Tools(ctx)
			if err != nil {
				return parity.Outcome{}, err
			}
			if strings.Join(names, ",") != strings.Join(protocol.MCPTools, ",") {
				return parity.Outcome{Class: "tools", Detail: strings.Join(names, ",")}, nil
			}
			calls := []struct {
				tool string
				args M
			}{
				{"js", M{"code": `nodeRepl.write(JSON.stringify(await fennec.status()))`, "title": "Checking status"}},
				{"js_reset", M{}},
				{"status", M{}},
				{"observe", M{}},
				{"act", M{"steps": []M{{"press": `button "Increment"`}}}},
				{"wait", M{"still": true, "timeout_ms": 1000}},
				{"run", M{"code": "export async function main() { return 41 + 1 }"}},
				{"flow", M{"op": "list"}},
			}
			for _, c := range calls {
				r, err := s.Tool(ctx, c.tool, c.args)
				if err != nil {
					return parity.Outcome{}, err
				}
				if r.IsError {
					return parity.Outcome{Class: "tool_error", Detail: c.tool + ": " + clip(r.Text(), 200)}, nil
				}
			}
			return parity.Outcome{OK: true}, nil
		},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			c.eq("presses.increment", r.State.Num("presses.increment"), 1)
			return c.err()
		},
	})
}

// CAP-RGB-005: a stop from the CLI ends a run in flight, and no tool
// lifts it.
func TestStopEndsRunInFlight(t *testing.T) {
	const loop = "export async function main() { for (;;) {} }"
	stopAfter := func(ctx context.Context, stop func() error) (time.Duration, error) {
		time.Sleep(time.Second)
		start := time.Now()
		return time.Since(start), stop()
	}
	run(t, Case{
		Caps:    []string{"CAP-RGB-005"},
		Scene:   "basic",
		Timeout: 3 * time.Minute,
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			paths := daemon.PathsIn(fennecRun())
			defer func() { _, _ = daemon.Control(context.Background(), paths, "resume") }()
			done := make(chan time.Time, 1)
			var text string
			go func() {
				r, _ := s.Tool(ctx, "run", M{"code": loop, "timeout_s": 60})
				text = r.Text()
				done <- time.Now()
			}()
			if _, err := stopAfter(ctx, func() error { _, err := daemon.Control(ctx, paths, "stop"); return err }); err != nil {
				return parity.Outcome{}, err
			}
			stopped := time.Now()
			select {
			case end := <-done:
				if !strings.Contains(text, "stopped") {
					return parity.Outcome{Class: "not_stopped", Detail: clip(text, 300)}, nil
				}
				// No tool lifts the stop.
				a, err := s.Steps(ctx, M{"press": `button "Increment"`})
				if err != nil {
					return parity.Outcome{}, err
				}
				if a.Code() != string(protocol.ErrStopped) {
					return parity.Outcome{Class: "not_held", Detail: "a call after the stop: " + a.Code()}, nil
				}
				return parity.Outcome{OK: true, Detail: fmt.Sprintf("the run ended %s after the stop", end.Sub(stopped))}, nil
			case <-time.After(10 * time.Second):
				return parity.Outcome{Class: "not_stopped", Detail: "the run kept going 10 s after the stop"}, nil
			}
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			ctl := func(op string) error {
				cmd := exec.CommandContext(ctx, shuttleBin, op)
				cmd.Env = shuttleEnv()
				return cmd.Run()
			}
			defer func() { _ = ctl("resume") }()
			done := make(chan time.Time, 1)
			var text string
			go func() {
				r, _ := s.Tool(ctx, "script_run", M{"code": loop, "timeout_s": 60})
				text = r.Text()
				done <- time.Now()
			}()
			if _, err := stopAfter(ctx, func() error { return ctl("stop") }); err != nil {
				return parity.Outcome{}, err
			}
			stopped := time.Now()
			select {
			case end := <-done:
				if !strings.Contains(text, "stopped") {
					return parity.Outcome{Class: "not_stopped", Detail: clip(text, 300)}, nil
				}
				p, err := s.Plan(ctx, M{"click": "Increment"})
				if err != nil {
					return parity.Outcome{}, err
				}
				if p.OK {
					return parity.Outcome{Class: "not_held", Detail: "a plan ran after the stop"}, nil
				}
				return parity.Outcome{OK: true, Detail: fmt.Sprintf("the run ended %s after the stop", end.Sub(stopped))}, nil
			case <-time.After(10 * time.Second):
				return parity.Outcome{Class: "not_stopped", Detail: "the run kept going 10 s after the stop"}, nil
			}
		},
		Keys: []string{"presses.increment"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			c.eq("presses.increment", r.State.Num("presses.increment"), 0)
			return c.err()
		},
	})
}

var _ = json.Marshal
