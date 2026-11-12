package vision

import (
	"context"
	"fmt"
	"math"
	"strings"

	"fennec/cua/internal/protocol"
)

// WindowTarget is a window whose image pixels a step names.
type WindowTarget struct {
	PID    int
	ID     uint32
	Bounds [4]int // desktop coordinates
	Layout string // the display layout the bounds were read under
}

func (w WindowTarget) key() WindowKey {
	return WindowKey{PID: w.PID, ID: w.ID, Bounds: w.Bounds, Layout: w.Layout}
}

// WindowScale is image pixels per desktop unit of a window capture, across
// and down; it holds on any display and any backing scale because both
// sides of the ratio come from the same window.
func WindowScale(c *Capture, bounds [4]int) ([2]float64, error) {
	if bounds[2] <= 0 || bounds[3] <= 0 || c.W <= 0 || c.H <= 0 {
		return [2]float64{}, &protocol.Error{Code: protocol.ErrWindowNotFound, Message: "the window has no size on screen", Hint: "restore or show the window first"}
	}
	return [2]float64{float64(c.W) / float64(bounds[2]), float64(c.H) / float64(bounds[3])}, nil
}

// WindowPixel maps a pixel of a window's image to desktop coordinates.
func WindowPixel(bounds [4]int, k [2]float64, x, y float64) (float64, float64) {
	return float64(bounds[0]) + x/k[0], float64(bounds[1]) + y/k[1]
}

// WindowMapper maps window-image pixels to desktop points, reusing the
// scale a read of the same window at the same bounds and display layout
// proved instead of capturing again. Its captures run in their own session,
// so they never change the scale another session's click is mapped by.
type WindowMapper struct {
	Capturer Capturer
	Scales   *WindowScales
	// Session is the capture session window reads use.
	Session string
}

// Map maps the window pixels pts (pairs of x, y) to desktop coordinates,
// reading the scale at most once for all of them.
func (m *WindowMapper) Map(ctx context.Context, w WindowTarget, pts ...[2]float64) ([][2]float64, error) {
	k, ok := m.Scales.Proven(w.key())
	if !ok {
		c, err := m.Capturer.Window(ctx, m.Session, w.PID, w.ID)
		if err != nil {
			return nil, err
		}
		if k, err = WindowScale(c, w.Bounds); err != nil {
			return nil, err
		}
		m.Scales.Prove(w.key(), k)
	}
	out := make([][2]float64, len(pts))
	for i, p := range pts {
		out[i][0], out[i][1] = WindowPixel(w.Bounds, k, p[0], p[1])
	}
	return out, nil
}

// UniqueTitle refuses window-pixel points by a title that two listed
// windows share, since the pixels could be the other window's.
func UniqueTitle(windows []protocol.Window, title string) error {
	n := 0
	for _, w := range windows {
		if strings.EqualFold(strings.TrimSpace(w.Title), strings.TrimSpace(title)) {
			n++
		}
	}
	if n > 1 {
		return &protocol.Error{
			Code:    protocol.ErrTargetAmbiguous,
			Message: fmt.Sprintf("%d windows are titled %q, so pixels of a shot by that title may be another window's", n, title),
			Hint:    "name the window by its ref, or use screen coordinates",
		}
	}
	return nil
}

// OffScreen refuses image points that map outside the screen before any
// input is sent, naming the screen in screenshot pixels and its valid
// ranges. pts are image pixels of shot.
func OffScreen(shot Shot, action string, pts ...[2]float64) error {
	sw, sh := shot.Screen[0], shot.Screen[1]
	if sw <= 0 || sh <= 0 || !Positive(shot.Scale) {
		return nil
	}
	for _, p := range pts {
		x, y := shot.ToDesktop(p[0], p[1])
		if x >= 0 && x < sw && y >= 0 && y < sh {
			continue
		}
		w := math.Floor((sw - shot.Origin[0]) / shot.Scale)
		h := math.Floor((sh - shot.Origin[1]) / shot.Scale)
		return &protocol.Error{
			Code: protocol.ErrInvalid,
			Message: fmt.Sprintf("(%s,%s) is off the screen, which is %.0fx%.0f screenshot pixels (x 0 to %.0f, y 0 to %.0f); the %s was not sent",
				num(p[0]), num(p[1]), w, h, w-1, h-1, action),
			Hint: "take a new screenshot and use a point inside it",
		}
	}
	return nil
}

func num(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%g", v)
}

// StateCrop is the rectangle of a capture that a visual check of a
// control's state looks at: the control with a margin, placed relative to
// the captured screen's origin so it holds on secondary displays and on
// displays left of or above the main one. bounds and origin are desktop
// coordinates, perPx is capture pixels per desktop unit and margin the
// horizontal and vertical margin in desktop units.
func StateCrop(bounds [4]float64, origin [2]float64, perPx float64, margin [2]float64) Rect {
	x := bounds[0] - margin[0] - origin[0]
	y := bounds[1] - margin[1] - origin[1]
	w := bounds[2] + 2*margin[0] + math.Min(0, x)
	h := bounds[3] + 2*margin[1] + math.Min(0, y)
	x, y = math.Max(0, x), math.Max(0, y)
	return Rect{int(math.Floor(x * perPx)), int(math.Floor(y * perPx)), int(math.Round(w * perPx)), int(math.Round(h * perPx))}
}
