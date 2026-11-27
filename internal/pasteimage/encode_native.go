//go:build cgo && !ios && !android

package pasteimage

import (
	"fmt"
	"image"
	"image/draw"

	nativewebp "github.com/chai2010/webp"
)

// native: the Mac daemon is built with cgo, so it encodes with libwebp 1.4.0
// compiled in (github.com/chai2010/webp). The transpiled fallback took about
// 1 s for a Retina screenshot and 2.3 s for a 5K one; this takes 0.2-0.4 s
// at the same method and size (FEN-191). gomobile (iOS) builds keep the
// fallback so the framework carries no extra C code.
const native = true

func encode(m image.Image) ([]byte, error) {
	n := straight(m)
	// WebPEncodeRGBA (method 4) wants non-premultiplied RGBA, which is
	// exactly NRGBA's layout; the image.RGBA wrapper only carries the bytes.
	out, err := nativewebp.EncodeRGBA(&image.RGBA{Pix: n.Pix, Stride: n.Stride, Rect: image.Rect(0, 0, n.Rect.Dx(), n.Rect.Dy())}, quality)
	if err != nil {
		return nil, fmt.Errorf("encode WebP: %w", err)
	}
	return out, nil
}

// straight returns m as non-premultiplied RGBA, reusing its pixels when they
// already are (NRGBA, or opaque RGBA where both forms are the same bytes).
func straight(m image.Image) *image.NRGBA {
	switch img := m.(type) {
	case *image.NRGBA:
		return img
	case *image.RGBA:
		if img.Opaque() {
			return &image.NRGBA{Pix: img.Pix, Stride: img.Stride, Rect: img.Rect}
		}
	}
	b := m.Bounds()
	n := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(n, n.Rect, m, b.Min, draw.Src)
	return n
}
