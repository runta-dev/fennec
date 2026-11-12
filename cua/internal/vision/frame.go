// Package vision handles fennec-cua's pixels: captures through cua-driver's
// capture sessions, image decoding, cropping, shrinking and encoding,
// mapping between image pixels and desktop coordinates, screen stillness,
// template matching and the numbered element list of vision mode.
//
// Desktop coordinates are pixels of the main display (accessibility points
// times its backing scale). Every image carries the map back to them:
// desktop = origin + pixel * scale.
//
// The package is platform-free: it reaches the screen only through the
// Capturer interface, which DriverCapturer implements over a cua-driver
// client.
package vision

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
)

// ErrOffImage is a crop rectangle that misses the image entirely.
var ErrOffImage = errors.New("the rectangle is off the image")

// Frame is an 8-bit RGB raster, three bytes per pixel, rows top to bottom.
// Captures keep raw pixels so each image is encoded once.
type Frame struct {
	W, H int
	Pix  []byte
}

// NewFrame returns a black frame.
func NewFrame(w, h int) *Frame {
	return &Frame{W: w, H: h, Pix: make([]byte, w*h*3)}
}

// RGB returns the pixel at x, y.
func (f *Frame) RGB(x, y int) (r, g, b uint8) {
	i := (y*f.W + x) * 3
	return f.Pix[i], f.Pix[i+1], f.Pix[i+2]
}

// Set paints the pixel at x, y.
func (f *Frame) Set(x, y int, r, g, b uint8) {
	i := (y*f.W + x) * 3
	f.Pix[i], f.Pix[i+1], f.Pix[i+2] = r, g, b
}

// Fill paints a rectangle, clipped to the frame.
func (f *Frame) Fill(r Rect, red, green, blue uint8) {
	c, ok := r.Clip(f.W, f.H)
	if !ok {
		return
	}
	for y := c.Y; y < c.Y+c.H; y++ {
		for x := c.X; x < c.X+c.W; x++ {
			f.Set(x, y, red, green, blue)
		}
	}
}

// Clone copies a frame.
func (f *Frame) Clone() *Frame {
	return &Frame{W: f.W, H: f.H, Pix: append([]byte(nil), f.Pix...)}
}

// FrameOf converts any image to RGB, dropping the alpha channel: the colour
// channels are kept as they are, not composited over a background.
func FrameOf(img image.Image) *Frame {
	b := img.Bounds()
	f := NewFrame(b.Dx(), b.Dy())
	switch src := img.(type) {
	case *image.NRGBA:
		for y := 0; y < f.H; y++ {
			row := src.Pix[(y)*src.Stride:]
			for x := 0; x < f.W; x++ {
				copy(f.Pix[(y*f.W+x)*3:], row[x*4:x*4+3])
			}
		}
	case *image.RGBA:
		for y := 0; y < f.H; y++ {
			row := src.Pix[(y)*src.Stride:]
			for x := 0; x < f.W; x++ {
				copy(f.Pix[(y*f.W+x)*3:], row[x*4:x*4+3])
			}
		}
	default:
		for y := 0; y < f.H; y++ {
			for x := 0; x < f.W; x++ {
				c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
				f.Set(x, y, c.R, c.G, c.B)
			}
		}
	}
	return f
}

// Image returns the frame as an opaque NRGBA image.
func (f *Frame) Image() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, f.W, f.H))
	for i, j := 0, 0; i < len(f.Pix); i, j = i+3, j+4 {
		img.Pix[j], img.Pix[j+1], img.Pix[j+2], img.Pix[j+3] = f.Pix[i], f.Pix[i+1], f.Pix[i+2], 0xff
	}
	return img
}

// DecodePNG reads a PNG into RGB pixels.
func DecodePNG(data []byte) (*Frame, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode the capture: %w", err)
	}
	return FrameOf(img), nil
}

// pngEncoder uses a fast deflate level: captures are big and short-lived.
var pngEncoder = png.Encoder{CompressionLevel: png.BestSpeed}

// EncodePNG writes the frame as an 8-bit RGB PNG.
func EncodePNG(f *Frame) ([]byte, error) {
	var buf bytes.Buffer
	if err := pngEncoder.Encode(&buf, f.Image()); err != nil {
		return nil, fmt.Errorf("encode PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// Rect is a pixel rectangle.
type Rect struct{ X, Y, W, H int }

// Clip cuts r to a w by h image; false when nothing is left.
func (r Rect) Clip(w, h int) (Rect, bool) {
	x0, y0 := max(r.X, 0), max(r.Y, 0)
	x1, y1 := min(r.X+r.W, w), min(r.Y+r.H, h)
	if x1 <= x0 || y1 <= y0 {
		return Rect{}, false
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}, true
}

// Crop returns the pixels of r clipped at the image edge and the rectangle
// actually cut; a rectangle wholly outside fails with ErrOffImage.
func (f *Frame) Crop(r Rect) (*Frame, Rect, error) {
	c, ok := r.Clip(f.W, f.H)
	if !ok {
		return nil, Rect{}, ErrOffImage
	}
	out := NewFrame(c.W, c.H)
	for y := 0; y < c.H; y++ {
		src := ((c.Y+y)*f.W + c.X) * 3
		copy(out.Pix[y*c.W*3:(y+1)*c.W*3], f.Pix[src:src+c.W*3])
	}
	return out, c, nil
}

// UnitRect converts a rectangle given in some unit (desktop coordinates,
// screen points) to the pixels of an image with factor pixels per unit:
// edges are scaled and floored, then clamped to the image. A rectangle that
// keeps no pixel fails with ErrOffImage.
func UnitRect(r [4]float64, factor float64, w, h int) (Rect, error) {
	clamp := func(v float64, hi int) int {
		v = math.Floor(v * factor)
		if v < 0 {
			return 0
		}
		if v > float64(hi) {
			return hi
		}
		return int(v)
	}
	left, top := clamp(r[0], w), clamp(r[1], h)
	right, bottom := clamp(r[0]+r[2], w), clamp(r[1]+r[3], h)
	if right <= left || bottom <= top {
		return Rect{}, ErrOffImage
	}
	return Rect{left, top, right - left, bottom - top}, nil
}

// FitSize is the size of a w by h image whose long edge is edge.
func FitSize(w, h, edge int) (int, int) {
	if w >= h {
		return edge, max(1, h*edge/w)
	}
	return max(1, w*edge/h), edge
}

// Fit shrinks the frame by area averaging so its long edge is edge. Each
// output pixel averages the source block it covers.
func Fit(f *Frame, edge int) *Frame {
	tw, th := FitSize(f.W, f.H, edge)
	out := NewFrame(tw, th)
	for oy := 0; oy < th; oy++ {
		y0 := oy * f.H / th
		y1 := max((oy+1)*f.H/th, y0+1)
		for ox := 0; ox < tw; ox++ {
			x0 := ox * f.W / tw
			x1 := max((ox+1)*f.W/tw, x0+1)
			var sum [3]int
			for y := y0; y < y1; y++ {
				row := (y*f.W + x0) * 3
				for i := row; i < row+(x1-x0)*3; i += 3 {
					sum[0] += int(f.Pix[i])
					sum[1] += int(f.Pix[i+1])
					sum[2] += int(f.Pix[i+2])
				}
			}
			n := (y1 - y0) * (x1 - x0)
			o := (oy*tw + ox) * 3
			out.Pix[o], out.Pix[o+1], out.Pix[o+2] = uint8(sum[0]/n), uint8(sum[1]/n), uint8(sum[2]/n)
		}
	}
	return out
}

// Lanczos shrinks the frame with a Lanczos3 filter so its long edge is
// edge; a frame already no larger is returned as it is.
func Lanczos(f *Frame, edge int) *Frame {
	if edge <= 0 || max(f.W, f.H) <= edge {
		return f
	}
	tw, th := FitSize(f.W, f.H, edge)
	return resize(f, tw, th)
}

// resize resamples separably: rows first, then columns.
func resize(f *Frame, tw, th int) *Frame {
	horiz := weights(f.W, tw)
	mid := make([]float32, tw*f.H*3)
	for y := 0; y < f.H; y++ {
		row := f.Pix[y*f.W*3:]
		for x, k := range horiz {
			var r, g, b float32
			for i, w := range k.w {
				p := (k.start + i) * 3
				r += w * float32(row[p])
				g += w * float32(row[p+1])
				b += w * float32(row[p+2])
			}
			o := (y*tw + x) * 3
			mid[o], mid[o+1], mid[o+2] = r, g, b
		}
	}
	vert := weights(f.H, th)
	out := NewFrame(tw, th)
	for y, k := range vert {
		for x := 0; x < tw; x++ {
			var r, g, b float32
			for i, w := range k.w {
				p := ((k.start+i)*tw + x) * 3
				r += w * mid[p]
				g += w * mid[p+1]
				b += w * mid[p+2]
			}
			o := (y*tw + x) * 3
			out.Pix[o], out.Pix[o+1], out.Pix[o+2] = clamp8(r), clamp8(g), clamp8(b)
		}
	}
	return out
}

type kernel struct {
	start int
	w     []float32
}

// weights are the normalized Lanczos3 taps of each output sample when n
// source samples shrink to m.
func weights(n, m int) []kernel {
	scale := float64(n) / float64(m)
	support := 3.0
	if scale > 1 {
		support *= scale
	}
	out := make([]kernel, m)
	for i := range out {
		center := (float64(i)+0.5)*scale - 0.5
		lo := max(int(math.Ceil(center-support)), 0)
		hi := min(int(math.Floor(center+support)), n-1)
		k := kernel{start: lo, w: make([]float32, 0, hi-lo+1)}
		var sum float64
		ws := make([]float64, 0, hi-lo+1)
		for j := lo; j <= hi; j++ {
			d := float64(j) - center
			if scale > 1 {
				d /= scale
			}
			v := lanczos3(d)
			ws = append(ws, v)
			sum += v
		}
		for _, v := range ws {
			k.w = append(k.w, float32(v/sum))
		}
		out[i] = k
	}
	return out
}

func lanczos3(x float64) float64 {
	x = math.Abs(x)
	if x < 1e-9 {
		return 1
	}
	if x >= 3 {
		return 0
	}
	px := math.Pi * x
	return 3 * math.Sin(px) * math.Sin(px/3) / (px * px)
}

func clamp8(v float32) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v + 0.5)
}
