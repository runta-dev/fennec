package vision

import (
	"context"
	"fmt"
	"math"
)

// Grounded checks: sizes in points, scaled by the backing scale.
const (
	CheckCropW = 640
	CheckCropH = 400
	RingPoints = 14
	// ZoomEdge is the long edge a close-up is magnified to at least.
	ZoomEdge = 384
)

// GroundCheck makes the images that check a grounded point before a click.
// Every image comes from a capture taken after grounding, and the point,
// crops and ring are placed in capture pixels.
type GroundCheck struct {
	Capturer Capturer
	Session  string
}

// CheckImage is one image of a grounded check and what its prompt says.
type CheckImage struct {
	Image  Encoded
	Prompt string
	// Pixel is the point in this image's pixels.
	Pixel [2]int
}

// capture takes one full capture and its capture pixels per desktop unit.
func (g GroundCheck) capture(ctx context.Context) (*Frame, *Capture, float64, error) {
	c, err := g.Capturer.Desktop(ctx, g.Session, 0)
	if err != nil {
		return nil, nil, 0, err
	}
	f, err := c.Pixels()
	if err != nil {
		return nil, nil, 0, err
	}
	return f, c, 1 / c.DesktopScale(), nil
}

// around places a w by h rectangle (capture pixels) centred on p inside
// the capture.
func around(p [2]float64, w, h int, f *Frame) Rect {
	w, h = min(w, f.W), min(h, f.H)
	x := int(math.Max(0, math.Min(float64(f.W-w), math.Round(p[0]-float64(w)/2))))
	y := int(math.Max(0, math.Min(float64(f.H-h), math.Round(p[1]-float64(h)/2))))
	return Rect{x, y, w, h}
}

func backingOf(c *Capture) float64 {
	if Positive(c.Backing) {
		return c.Backing
	}
	return 1
}

// CloseUp takes one capture and cuts a CheckCropW by CheckCropH point crop
// around the desktop point x, y.
func (g GroundCheck) CloseUp(ctx context.Context, x, y float64) (CheckImage, error) {
	f, c, per, err := g.capture(ctx)
	if err != nil {
		return CheckImage{}, err
	}
	b := backingOf(c) * per
	p := [2]float64{x * per, y * per}
	r := around(p, int(CheckCropW*b), int(CheckCropH*b), f)
	cut, _, err := f.Crop(r)
	if err != nil {
		return CheckImage{}, err
	}
	px := [2]int{int(math.Round(p[0])) - r.X, int(math.Round(p[1])) - r.Y}
	enc, err := Encode(cut, FormatPNG)
	if err != nil {
		return CheckImage{}, err
	}
	return CheckImage{Image: enc, Pixel: px, Prompt: fmt.Sprintf("A %dx%d crop of the screen. A click is about to land on its pixel (%d, %d).", cut.W, cut.H, px[0], px[1])}, nil
}

// Full takes a single full-screen capture and cuts both checks from it: a
// magnified close-up around the point (sized by the element's bounds in
// desktop coordinates, when known) and the whole screen with a ring at
// the point.
func (g GroundCheck) Full(ctx context.Context, x, y float64, element *[4]float64) (zoom, ringed CheckImage, err error) {
	f, c, per, err := g.capture(ctx)
	if err != nil {
		return CheckImage{}, CheckImage{}, err
	}
	b := backingOf(c) * per
	p := [2]float64{x * per, y * per}
	rw := 192 * b
	if element != nil && element[2] > 0 && element[3] > 0 {
		ew, eh := element[2]*per, element[3]*per
		rw = math.Min(384*b, math.Max(96*b, math.Max(1.5*ew, 5*eh*4/3)))
	}
	rh := rw * 3 / 4
	r := around(p, int(math.Round(rw)), int(math.Round(rh)), f)
	cut, _, err := f.Crop(r)
	if err != nil {
		return CheckImage{}, CheckImage{}, err
	}
	mag := max(1, int(math.Ceil(ZoomEdge/float64(max(cut.W, 1)))))
	big := magnify(cut, mag)
	zpx := [2]int{(int(math.Round(p[0])) - r.X) * mag, (int(math.Round(p[1])) - r.Y) * mag}
	if zoom.Image, err = Encode(big, FormatPNG); err != nil {
		return CheckImage{}, CheckImage{}, err
	}
	zoom.Pixel = zpx
	zoom.Prompt = fmt.Sprintf("A %dx%d crop of the screen. A click is about to land on its pixel (%d, %d).", big.W, big.H, zpx[0], zpx[1])
	full := f.Clone()
	rpx := [2]int{int(math.Round(p[0])), int(math.Round(p[1]))}
	Ring(full, rpx[0], rpx[1], int(math.Round(RingPoints*b)))
	if ringed.Image, err = Encode(full, FormatPNG); err != nil {
		return CheckImage{}, CheckImage{}, err
	}
	ringed.Pixel = rpx
	ringed.Prompt = fmt.Sprintf("The screen, %dx%d. A red ring with four ticks marks the spot a click is about to land on, centred at pixel (%d, %d).", f.W, f.H, rpx[0], rpx[1])
	return zoom, ringed, nil
}

// magnify scales a frame up by an integer factor, pixel by pixel.
func magnify(f *Frame, k int) *Frame {
	if k <= 1 {
		return f
	}
	out := NewFrame(f.W*k, f.H*k)
	for y := 0; y < out.H; y++ {
		for x := 0; x < out.W; x++ {
			r, g, b := f.RGB(x/k, y/k)
			out.Set(x, y, r, g, b)
		}
	}
	return out
}

// Ring paints a red ring of radius r around cx, cy with four ticks pointing
// at it, leaving the centre visible.
func Ring(f *Frame, cx, cy, r int) {
	t := max(2, r/7)
	tick, length := r+3*t, 3*r
	reach := tick + length + 2
	for y := max(0, cy-reach); y <= min(f.H-1, cy+reach); y++ {
		for x := max(0, cx-reach); x <= min(f.W-1, cx+reach); x++ {
			dx, dy := math.Abs(float64(x-cx)), math.Abs(float64(y-cy))
			onRing := math.Abs(math.Hypot(dx, dy)-float64(r)) <= float64(t)/2
			onTick := (dx <= float64(t)/2 && dy >= float64(tick) && dy <= float64(tick+length)) ||
				(dy <= float64(t)/2 && dx >= float64(tick) && dx <= float64(tick+length))
			if onRing || onTick {
				f.Set(x, y, 255, 0, 0)
			}
		}
	}
}
