// Command cua-icon makes the standalone Fennec CUA app icon from Fennec's:
// the Fennec fox on its white tile with a small computer-use badge (a
// pointer on a dark disc with a white ring) in the bottom-right corner.
//
//	go run ./cmd/cua-icon [-src ../native/macos/AppIcon.png] [-out assets]
//
// It writes OUT/FennecCUA.png (1024 px), OUT/FennecCUA.iconset (every
// size iconutil needs; an intermediate, not kept in the repository) and
// OUT/FennecCUA.icns (through /usr/bin/iconutil).
// cmd/cua-dist ships the .icns as Contents/Resources/FennecCUA.icns.
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/vector"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cua-icon:", err)
		os.Exit(1)
	}
}

// Fennec's colours (native/macos/AppIcon.svg): the fox's dark brown and
// its orange.
var (
	badgeFill = color.NRGBA{0x3B, 0x33, 0x2E, 0xFF}
	ring      = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	accent    = color.NRGBA{0xF3, 0x8D, 0x25, 0xFF}
)

func run(args []string) error {
	fs := flag.NewFlagSet("cua-icon", flag.ContinueOnError)
	src := fs.String("src", filepath.Join("..", "native", "macos", "AppIcon.png"), "Fennec's 1024 px app icon")
	out := fs.String("out", "assets", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	f, err := os.Open(*src)
	if err != nil {
		return err
	}
	base, err := png.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	if b := base.Bounds(); b.Dx() != 1024 || b.Dy() != 1024 {
		return errors.New("the source icon must be 1024 x 1024")
	}
	icon := Badge(base)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	if err := writePNG(filepath.Join(*out, "FennecCUA.png"), icon); err != nil {
		return err
	}
	set := filepath.Join(*out, "FennecCUA.iconset")
	if err := os.RemoveAll(set); err != nil {
		return err
	}
	if err := os.MkdirAll(set, 0o755); err != nil {
		return err
	}
	for _, s := range []int{16, 32, 128, 256, 512} {
		for _, scale := range []int{1, 2} {
			name := fmt.Sprintf("icon_%dx%d.png", s, s)
			if scale == 2 {
				name = fmt.Sprintf("icon_%dx%d@2x.png", s, s)
			}
			if err := writePNG(filepath.Join(set, name), resize(icon, s*scale)); err != nil {
				return err
			}
		}
	}
	icns := filepath.Join(*out, "FennecCUA.icns")
	if o, err := exec.Command("/usr/bin/iconutil", "-c", "icns", "-o", icns, set).CombinedOutput(); err != nil {
		return fmt.Errorf("iconutil: %v: %s", err, o)
	}
	fmt.Println("wrote", icns)
	return nil
}

// Badge draws the computer-use badge onto a copy of a 1024 px icon.
func Badge(base image.Image) *image.NRGBA {
	const size = 1024
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	xdraw.Draw(dst, dst.Bounds(), base, base.Bounds().Min, xdraw.Src)
	cx, cy := float32(806), float32(806)
	fill(dst, circle(cx, cy, 150), ring)
	fill(dst, circle(cx, cy, 132), badgeFill)
	// A thin orange ring inside the disc ties it to the fox.
	fillRing(dst, cx, cy, 120, 110, accent)
	// The pointer: the classic arrow, tip up-left, centred in the disc.
	pts := [][2]float32{{0, 0}, {0, 16}, {4, 12.4}, {6.9, 19}, {9.6, 17.8}, {6.8, 11.4}, {12, 11.4}}
	const scale = 8.6
	ox, oy := cx-5*scale, cy-9.5*scale
	var path [][2]float32
	for _, p := range pts {
		path = append(path, [2]float32{ox + p[0]*scale, oy + p[1]*scale})
	}
	fill(dst, polygon(path), ring)
	return dst
}

type shape func(r *vector.Rasterizer)

func fill(dst *image.NRGBA, s shape, c color.NRGBA) {
	b := dst.Bounds()
	r := vector.NewRasterizer(b.Dx(), b.Dy())
	r.DrawOp = xdraw.Over
	s(r)
	r.Draw(dst, b, image.NewUniform(c), image.Point{})
}

func circle(cx, cy, rad float32) shape {
	return func(r *vector.Rasterizer) {
		const n = 256
		for i := 0; i <= n; i++ {
			a := 2 * math.Pi * float64(i) / n
			x, y := cx+rad*float32(math.Cos(a)), cy+rad*float32(math.Sin(a))
			if i == 0 {
				r.MoveTo(x, y)
			} else {
				r.LineTo(x, y)
			}
		}
		r.ClosePath()
	}
}

// fillRing fills the annulus between outer and inner radii.
func fillRing(dst *image.NRGBA, cx, cy, outer, inner float32, c color.NRGBA) {
	b := dst.Bounds()
	ringImg := image.NewNRGBA(b)
	fill(ringImg, circle(cx, cy, outer), c)
	hole := image.NewAlpha(b)
	r := vector.NewRasterizer(b.Dx(), b.Dy())
	circle(cx, cy, inner)(r)
	r.Draw(hole, b, image.Opaque, image.Point{})
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			a := hole.AlphaAt(x, y).A
			if a == 0 {
				continue
			}
			o := ringImg.PixOffset(x, y)
			ringImg.Pix[o+3] = uint8(uint16(ringImg.Pix[o+3]) * uint16(255-a) / 255)
		}
	}
	xdraw.Draw(dst, b, ringImg, image.Point{}, xdraw.Over)
}

func polygon(pts [][2]float32) shape {
	return func(r *vector.Rasterizer) {
		r.MoveTo(pts[0][0], pts[0][1])
		for _, p := range pts[1:] {
			r.LineTo(p[0], p[1])
		}
		r.ClosePath()
	}
}

func resize(src image.Image, px int) image.Image {
	if px == src.Bounds().Dx() {
		return src
	}
	dst := image.NewNRGBA(image.Rect(0, 0, px, px))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return dst
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
