//go:build scenario

package scenario

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/ax"
	"fennec/cua/internal/parity"
)

// coverPID is the pid of the cover scene's other app.
func coverPID(ctx context.Context) (int, error) {
	st, err := h.fx.RawState(ctx)
	if err != nil {
		return 0, err
	}
	pid := int(State(st).Num("volatile.cover_pid"))
	if pid == 0 {
		return 0, errors.New("the cover app has no pid")
	}
	return pid, nil
}

// coverOver moves the other app's cover window so it covers the centre
// of a fixture element of the Covered Target window.
func coverOver(ctx context.Context, role, name string) (int, int, error) {
	b, err := boundsOf(ctx, "Covered Target", role, name)
	if err != nil {
		return 0, 0, err
	}
	x, y := center(b)
	pid, err := coverPID(ctx)
	if err != nil {
		return 0, 0, err
	}
	s := ax.BackingScale()
	if err := moveWindow(pid, "Other App Cover", float64(x)/s-60, float64(y)/s-40); err != nil {
		return 0, 0, err
	}
	return x, y, sleep(ctx, 400)
}

// coverCenter is the centre of the other app's cover window.
func coverCenter(ctx context.Context) (int, int, error) {
	// The cover is an accessory app's floating window, which the window
	// server lists though accessibility may not.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sw, _ := ax.ServerWindows(true)
		for _, w := range sw {
			if w.Title == "Other App Cover" && w.Bounds.W > 0 {
				s := ax.BackingScale()
				return int((w.Bounds.X + w.Bounds.W/2) * s), int((w.Bounds.Y + w.Bounds.H/2 + 10) * s), nil
			}
		}
		if err := sleep(ctx, 100); err != nil {
			return 0, 0, err
		}
	}
	return 0, 0, errors.New("the window server lists no Other App Cover")
}

func padClicks(st State, pad string) int {
	n := 0
	for _, c := range st.Events("clicks") {
		if c["pad"] == pad {
			n++
		}
	}
	return n
}

// CAP-RGB-023: a background press reaches a covered button in its own
// window, never the cover.
func TestBackgroundPressCovered(t *testing.T) {
	run(t, Case{
		Caps:   []string{"CAP-RGB-023"},
		Scene:  "cover",
		Config: Config{Input: "background"},
		Setup: func(ctx context.Context) error {
			if _, _, err := coverOver(ctx, "button", "Under Cover"); err != nil {
				return err
			}
			return frontFinder(ctx)
		},
		Fennec:  fennecPlan(M{"press": `button "Under Cover"`, "window": "Covered Target"}),
		Shuttle: shuttlePlan(M{"app": "CUAFixture", "title": "Covered Target"}, M{"click": "Under Cover"}),
		Keys:    []string{"presses.under-cover", "events.cover_clicks"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("presses.under-cover", r.State.Num("presses.under-cover"), 1)
			c.eq("cover clicks", len(r.State.Events("cover_clicks")), 0)
			c.eq("active", r.State.Bool("active"), false)
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-038: a click on a covered text field never lands on the cover.
func TestTextFieldCoveredRefused(t *testing.T) {
	run(t, Case{
		Caps:    []string{"CAP-RGB-038"},
		Scene:   "cover",
		Setup:   func(ctx context.Context) error { _, _, err := coverOver(ctx, "textbox", "Covered Field"); return err },
		Fennec:  fennecPlan(M{"click": `textbox "Covered Field"`, "window": "Covered Target"}, M{"type": "zz"}),
		Shuttle: shuttlePlan(M{"click": "Covered Field"}, M{"type": "zz"}),
		Keys:    []string{"events.cover_clicks"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("cover clicks", len(r.State.Events("cover_clicks")), 0)
			if r.Outcome.OK {
				c.eq("controls.covered-field", r.State.Str("controls.covered-field"), "zz")
			}
			return c.err()
		},
	})
}

// CAP-RGB-042: a hit test at a covered point names the covering window.
func TestPointCoveredByOtherWindow(t *testing.T) {
	var x, y int
	run(t, Case{
		Caps:  []string{"CAP-RGB-042"},
		Scene: "cover",
		Setup: func(ctx context.Context) (err error) { x, y, err = coverCenter(ctx); return err },
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			o, perr, err := s.Observe(ctx, M{"scope": "point", "window": "Covered Target", "target": pt(x, y)})
			if err != nil {
				return parity.Outcome{}, err
			}
			if perr != nil {
				return parity.Outcome{Class: string(perr.Code), Detail: perr.Message}, nil
			}
			if o.At == nil || o.At.Covered == nil || o.At.Covered.Title != "Other App Cover" {
				return parity.Outcome{Class: "no_cover", Detail: fmt.Sprintf("%+v", o.At)}, nil
			}
			return parity.Outcome{OK: true}, nil
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			d, err := s.Desk(ctx, "at", M{"x": x, "y": y, "window": "Covered Target"})
			if err != nil {
				return parity.Outcome{}, err
			}
			if d.IsError || !strings.Contains(d.Text, `"covered"`) || !strings.Contains(d.Text, "Other App Cover") {
				return parity.Outcome{Class: "no_cover", Detail: clip(d.Text, 300)}, nil
			}
			return parity.Outcome{OK: true}, nil
		},
		Keys: []string{"events.cover_clicks"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	})
}

// CAP-RGB-054: a foreground click whose point another app's floating window
// covers goes to the target window behind, never the cover.
func TestFloatingPanelCoversPoint(t *testing.T) {
	var x, y int
	run(t, Case{
		Caps:  []string{"CAP-RGB-054"},
		Scene: "cover",
		Setup: func(ctx context.Context) (err error) { x, y, err = coverCenter(ctx); return err },
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return fennecPlan(M{"click": pt(x, y), "window": "Covered Target"})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return shuttleDesk("click", M{"x": x, "y": y})(ctx, s)
		},
		Keys: []string{"events.cover_clicks"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("cover clicks", len(r.State.Events("cover_clicks")), 0)
			c.eq("Target Pad clicks", padClicks(r.State, "Target Pad"), 1)
			return c.err()
		},
	})
}

// CAP-RGA-049: a window click is never let through onto another app that
// covers the point.
func TestWindowClickCoveredRefused(t *testing.T) {
	var x, y int
	run(t, Case{
		Caps:  []string{"CAP-RGA-049"},
		Scene: "cover",
		Setup: func(ctx context.Context) (err error) { x, y, err = coverCenter(ctx); return err },
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return fennecPlan(M{"click": pt(x, y), "window": "Covered Target"})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return shuttleDesk("click", M{"x": x, "y": y, "window": "Covered Target"})(ctx, s)
		},
		Keys: []string{"events.cover_clicks"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("cover clicks", len(r.State.Events("cover_clicks")), 0)
			c.that(padClicks(r.State, "Target Pad") <= 1, "the target got %d clicks", padClicks(r.State, "Target Pad"))
			return c.err()
		},
	})
}

// CAP-RGA-048: a foreground click naming a window covered by another of
// its app's windows fronts it first, so the click lands on it.
func TestForegroundClickCoveredWindow(t *testing.T) {
	var x, y int
	setup := func(ctx context.Context) error {
		own, err := window(ctx, "CUAFixture", "Same App Cover")
		if err != nil {
			return err
		}
		pad, err := boundsOf(ctx, "Covered Target", "", "Target Pad")
		if err != nil {
			return err
		}
		ob := *own.Bounds
		// The middle of the cover's part over the pad.
		x0, x1 := max(ob[0], pad[0]), min(ob[0]+ob[2], pad[0]+pad[2])
		y0, y1 := max(ob[1], pad[1]), min(ob[1]+ob[3], pad[1]+pad[3])
		if x0 >= x1 || y0 >= y1 {
			return fmt.Errorf("Same App Cover %v does not cover Target Pad %v", ob, pad)
		}
		x, y = (x0+x1)/2, (y0+y1)/2+10
		return nil
	}
	run(t, Case{
		Caps:  []string{"CAP-RGA-048"},
		Scene: "cover",
		Setup: setup,
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return fennecPlan(M{"click": pt(x, y), "window": "Covered Target"})(ctx, s)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			return shuttleDesk("click", M{"x": x, "y": y, "window": "Covered Target"})(ctx, s)
		},
		Keys: []string{"events.clicks"},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.eq("Target Pad clicks", padClicks(r.State, "Target Pad"), 1)
			c.eq("Same App Cover clicks", padClicks(r.State, "Same App Cover"), 0)
			return c.err()
		},
	})
}

// CAP-RGB-005: under an allowlist a screen capture blacks out other apps.
func TestAllowlistBlackout(t *testing.T) {
	var cx, cy int
	inspect := func(data []byte, ox, oy, scale float64) (parity.Outcome, error) {
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return parity.Outcome{Class: "no_image", Detail: err.Error()}, nil
		}
		px, py := int((float64(cx)-ox)/scale), int((float64(cy)-oy)/scale)
		if !(image.Point{px, py}.In(img.Bounds())) {
			return parity.Outcome{Class: "off_image", Detail: fmt.Sprintf("%d,%d outside %v", px, py, img.Bounds())}, nil
		}
		r, g, b, _ := img.At(px, py).RGBA()
		if r>>8 > 8 || g>>8 > 8 || b>>8 > 8 {
			return parity.Outcome{Class: "not_blacked_out", Detail: fmt.Sprintf("pixel %d,%d is %d,%d,%d", px, py, r>>8, g>>8, b>>8)}, nil
		}
		return parity.Outcome{OK: true}, nil
	}
	run(t, Case{
		Caps:   []string{"CAP-RGB-005", "CAP-DSK-031"},
		Scene:  "cover",
		Config: Config{Allow: []string{"CUAFixture"}},
		Setup:  func(ctx context.Context) (err error) { cx, cy, err = coverCenter(ctx); return err },
		Fennec: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			o, data, perr, err := s.ObserveImage(ctx, M{"scope": "desktop", "image": "screen"})
			if err != nil {
				return parity.Outcome{}, err
			}
			if perr != nil {
				return parity.Outcome{Class: string(perr.Code), Detail: perr.Message}, nil
			}
			if o.Image == nil || data == nil {
				return parity.Outcome{Class: "no_image"}, nil
			}
			return inspect(data, o.Image.Origin[0], o.Image.Origin[1], o.Image.Scale)
		},
		Shuttle: func(ctx context.Context, s *Session) (parity.Outcome, error) {
			r, err := s.Tool(ctx, "screenshot", M{})
			if err != nil {
				return parity.Outcome{}, err
			}
			data, _ := firstImage(r)
			if r.IsError || data == nil {
				return parity.Outcome{Class: classify(r.Text()), Detail: clip(r.Text(), 300)}, nil
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				return parity.Outcome{Class: "no_image", Detail: err.Error()}, nil
			}
			w, err := windows(ctx)
			if err != nil {
				return parity.Outcome{}, err
			}
			_ = w
			screen := screenWidth()
			return inspect(data, 0, 0, float64(screen)/float64(cfg.Width))
		},
		Keys: []string{},
		Check: func(ctx context.Context, r *Result) error {
			var c checks
			c.ok(r)
			return c.err()
		},
	})
}

// screenWidth is the main display's width in desktop pixels.
func screenWidth() int {
	w, _, _ := mainDisplay()
	return w
}
