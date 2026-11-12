package policy

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"reflect"
	"strings"

	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// Allowlist admits apps by name, ignoring case. An empty allowlist admits
// every app.
type Allowlist struct {
	apps []string
}

// NewAllowlist makes an allowlist of app names.
func NewAllowlist(apps []string) Allowlist {
	var out []string
	for _, a := range apps {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return Allowlist{apps: out}
}

// Empty says whether every app is admitted.
func (a Allowlist) Empty() bool { return len(a.apps) == 0 }

// Apps lists the admitted apps.
func (a Allowlist) Apps() []string { return append([]string(nil), a.apps...) }

// Admits says whether an app is admitted.
func (a Allowlist) Admits(app string) bool {
	if a.Empty() {
		return true
	}
	app = strings.TrimSpace(app)
	for _, x := range a.apps {
		if strings.EqualFold(x, app) {
			return true
		}
	}
	return false
}

// Filter keeps only admitted apps' windows, so nothing else is listed,
// outlined, given a ref or named in a not-found error.
func (a Allowlist) Filter(ws []view.RawWindow) []view.RawWindow {
	if a.Empty() {
		return ws
	}
	out := make([]view.RawWindow, 0, len(ws))
	for _, w := range ws {
		if a.Admits(w.App) {
			out = append(out, w)
		}
	}
	return out
}

func (a Allowlist) refuse(what string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.ErrNotAllowed,
		Message: what + " is outside the app allowlist; nothing was sent",
		Hint:    "the user admits apps in Fennec's settings; act only in " + strings.Join(a.apps, ", "),
	}
}

// Window refuses a step whose target window, or for untargeted input the
// front window, belongs to an app outside the allowlist.
func (a Allowlist) Window(w *view.RawWindow) *protocol.Error {
	if a.Empty() {
		return nil
	}
	if w == nil {
		return a.refuse("the input has no admitted target window: the desktop")
	}
	if !a.Admits(w.App) {
		return a.refuse(fmt.Sprintf("window %q of %s", w.Title, appName(w.App)))
	}
	return nil
}

func appName(app string) string {
	if app == "" {
		return "an unnamed app"
	}
	return app
}

// Points gates a pointer step on every point it touches (both ends of a
// drag, the point of a scroll or click): the topmost window there, from a
// front-to-back list, must be admitted, and a point over no window is
// refused.
func (a Allowlist) Points(ws []view.RawWindow, pts ...[2]int) *protocol.Error {
	if a.Empty() {
		return nil
	}
	for _, p := range pts {
		w, err := view.WindowAt(ws, p[0], p[1])
		if err != nil {
			return a.refuse(fmt.Sprintf("the point (%d, %d), over no app window,", p[0], p[1]))
		}
		if !a.Admits(w.App) {
			return a.refuse(fmt.Sprintf("the point (%d, %d), over %s,", p[0], p[1], appName(w.App)))
		}
	}
	return nil
}

// Launch refuses launching an app outside the allowlist and, under an
// allowlist, any argument that starts with "-": options can make a browser
// run arbitrary programs. URLs and plain arguments pass.
func (a Allowlist) Launch(app string, args []string) *protocol.Error {
	if a.Empty() {
		return nil
	}
	if !a.Admits(app) {
		return a.refuse("launching " + appName(app))
	}
	for _, arg := range args {
		if strings.HasPrefix(strings.TrimSpace(arg), "-") {
			e := a.refuse(fmt.Sprintf("the launch option %q", arg))
			e.Hint = "under an app allowlist launch takes URLs and plain arguments only"
			return e
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Capture blackout

// CaptureTries bounds how often a masked capture is retried until the window
// set held still across it.
const CaptureTries = 3

// ErrUnstable means the windows kept moving across every masked capture.
var ErrUnstable = errors.New("windows kept moving while the screen was captured; nothing was shown, try again")

// Capture is one screen or region capture: its pixels and the desktop
// coordinates of its top-left pixel and pixels per desktop unit.
type Capture struct {
	Image  draw.Image
	Origin [2]float64
	Scale  float64
}

// Blackout paints every pixel of c outside the bounds of admitted windows
// black. ws is the window list, front to back; a window of another app on
// top of an admitted one is painted black where it covers it.
func (a Allowlist) Blackout(c Capture, ws []view.RawWindow) {
	if a.Empty() || c.Image == nil {
		return
	}
	b := c.Image.Bounds()
	scale := c.Scale
	if scale <= 0 {
		scale = 1
	}
	keep := make([]bool, b.Dx()*b.Dy())
	// Back to front, so a window on top decides its own pixels.
	for i := len(ws) - 1; i >= 0; i-- {
		w := ws[i]
		if w.Bounds == nil || !view.Shown(w) {
			continue
		}
		admit := a.Admits(w.App)
		r := w.Bounds
		x0 := int((float64(r[0]) - c.Origin[0]) / scale)
		y0 := int((float64(r[1]) - c.Origin[1]) / scale)
		x1 := int((float64(r[0]+r[2]) - c.Origin[0]) / scale)
		y1 := int((float64(r[1]+r[3]) - c.Origin[1]) / scale)
		for y := max(y0, 0); y < min(y1, b.Dy()); y++ {
			for x := max(x0, 0); x < min(x1, b.Dx()); x++ {
				keep[y*b.Dx()+x] = admit
			}
		}
	}
	black := color.RGBA{A: 255}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			if !keep[y*b.Dx()+x] {
				c.Image.Set(b.Min.X+x, b.Min.Y+y, black)
			}
		}
	}
}

// MaskedCapture takes a capture that shows only admitted apps. Without an
// allowlist it captures once and lists nothing. With one, it lists the
// windows before and after capturing and masks only with a list that stayed
// the same across the capture, retrying up to CaptureTries times.
func (a Allowlist) MaskedCapture(ctx context.Context, list func(context.Context) ([]view.RawWindow, error), capture func(context.Context) (Capture, error)) (Capture, error) {
	if a.Empty() {
		return capture(ctx)
	}
	for try := 0; try < CaptureTries; try++ {
		before, err := list(ctx)
		if err != nil {
			return Capture{}, err
		}
		c, err := capture(ctx)
		if err != nil {
			return Capture{}, err
		}
		after, err := list(ctx)
		if err != nil {
			return Capture{}, err
		}
		if sameWindows(before, after) {
			a.Blackout(c, after)
			return c, nil
		}
	}
	return Capture{}, ErrUnstable
}

func sameWindows(a, b []view.RawWindow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].App != b[i].App || a[i].Hidden != b[i].Hidden || a[i].Space != b[i].Space || !reflect.DeepEqual(a[i].Bounds, b[i].Bounds) {
			return false
		}
	}
	return true
}

// SolidImage is a helper for callers and tests: an RGBA image of one color.
func SolidImage(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	return img
}
