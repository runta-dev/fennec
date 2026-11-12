package vision

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/image/webp"
)

func sameFrame(t *testing.T, got, want *Frame) {
	t.Helper()
	if got.W != want.W || got.H != want.H {
		t.Fatalf("size %dx%d, want %dx%d", got.W, got.H, want.W, want.H)
	}
	if !bytes.Equal(got.Pix, want.Pix) {
		for i := range got.Pix {
			if got.Pix[i] != want.Pix[i] {
				t.Fatalf("pixel %d (byte %d) is %d, want %d", i/3, i, got.Pix[i], want.Pix[i])
			}
		}
	}
}

func TestPNGRoundTrip(t *testing.T) {
	for _, f := range []*Frame{noise(37, 23, 1), ui(300, 200), solid(1, 1, 9, 8, 7)} {
		data := mustPNG(t, f)
		if data[25] != 2 {
			t.Fatalf("PNG colour type %d, want 2 (RGB)", data[25])
		}
		back, err := DecodePNG(data)
		if err != nil {
			t.Fatal(err)
		}
		sameFrame(t, back, f)
	}
}

func TestWebPLosslessRoundTrip(t *testing.T) {
	for _, f := range []*Frame{noise(37, 23, 2), ui(640, 400), solid(1, 1, 1, 2, 3), solid(3, 300, 0, 0, 0)} {
		data, err := EncodeWebP(f)
		if err != nil {
			t.Fatal(err)
		}
		img, err := webp.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%dx%d: %v", f.W, f.H, err)
		}
		sameFrame(t, FrameOf(img), f)
		if format, w, h, ok := ImageSize(data); !ok || format != FormatWebP || w != f.W || h != f.H {
			t.Fatalf("ImageSize = %s %dx%d %v", format, w, h, ok)
		}
	}
}

func TestCropScalesAndClamps(t *testing.T) {
	f := NewFrame(8, 4)
	for i := range f.Pix {
		f.Pix[i] = uint8(i)
	}
	// A rectangle in screen points on a 2x capture.
	r, err := UnitRect([4]float64{1, 0.5, 2, 1}, 2, f.W, f.H)
	if err != nil || r != (Rect{2, 1, 4, 2}) {
		t.Fatalf("rect %+v %v", r, err)
	}
	cut, _, err := f.Crop(r)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodePNG(mustPNG(t, cut))
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			r1, g1, b1 := back.RGB(x, y)
			r2, g2, b2 := f.RGB(2+x, 1+y)
			if r1 != r2 || g1 != g2 || b1 != b2 {
				t.Fatalf("pixel %d,%d differs from the source", x, y)
			}
		}
	}
	// Clamped to the image.
	if r, err := UnitRect([4]float64{3, 1, 10, 10}, 2, f.W, f.H); err != nil || r != (Rect{6, 2, 2, 2}) {
		t.Fatalf("clamped %+v %v", r, err)
	}
	if r, err := UnitRect([4]float64{-1, -1, 2, 2}, 2, f.W, f.H); err != nil || r != (Rect{0, 0, 2, 2}) {
		t.Fatalf("clamped at the origin %+v %v", r, err)
	}
	// Wholly outside.
	for _, out := range [][4]float64{{10, 0, 2, 2}, {0, 5, 1, 1}, {-5, 0, 2, 2}} {
		if _, err := UnitRect(out, 2, f.W, f.H); !errors.Is(err, ErrOffImage) {
			t.Fatalf("%v: %v, want ErrOffImage", out, err)
		}
	}
}

func TestCropClipsAtImageEdge(t *testing.T) {
	f := noise(100, 50, 3)
	cut, got, err := f.Crop(Rect{90, 40, 30, 30})
	if err != nil || got != (Rect{90, 40, 10, 10}) || cut.W != 10 || cut.H != 10 {
		t.Fatalf("clip %+v %v", got, err)
	}
	r, g, b := cut.RGB(9, 9)
	r2, g2, b2 := f.RGB(99, 49)
	if r != r2 || g != g2 || b != b2 {
		t.Fatal("the clipped crop does not hold the source pixels")
	}
	if _, _, err := f.Crop(Rect{100, 0, 5, 5}); !errors.Is(err, ErrOffImage) {
		t.Fatalf("a rectangle wholly outside: %v", err)
	}
}

func TestFitAveragesBlocks(t *testing.T) {
	f := NewFrame(4, 2)
	vals := []uint8{0, 10, 100, 200, 20, 30, 50, 50}
	for i, v := range vals {
		f.Set(i%4, i/4, v, v/2, 255-v)
	}
	out := Fit(f, 2)
	if out.W != 2 || out.H != 1 {
		t.Fatalf("size %dx%d", out.W, out.H)
	}
	if r, _, _ := out.RGB(0, 0); r != (0+10+20+30)/4 {
		t.Fatalf("left block %d", r)
	}
	if r, _, b := out.RGB(1, 0); r != (100+200+50+50)/4 || b != uint8((155+55+205+205)/4) {
		t.Fatalf("right block %d %d", r, b)
	}
}

func TestFittedShrinksToLongEdge(t *testing.T) {
	big := solid(2560, 1600, 40, 80, 120)
	small := Lanczos(big, 1024)
	if small.W != 1024 || small.H != 640 {
		t.Fatalf("landscape %dx%d", small.W, small.H)
	}
	if r, g, b := small.RGB(500, 300); r != 40 || g != 80 || b != 120 {
		t.Fatalf("a flat image changed colour: %d %d %d", r, g, b)
	}
	if tall := Lanczos(solid(1000, 2000, 0, 0, 0), 1024); tall.W != 512 || tall.H != 1024 {
		t.Fatalf("portrait %dx%d", tall.W, tall.H)
	}
	f := solid(800, 500, 1, 1, 1)
	if Lanczos(f, 1024) != f {
		t.Fatal("an image within max_edge was resized")
	}
	if Lanczos(big, 0) != big {
		t.Fatal("max_edge 0 resized")
	}
	c := desktopCapture(t, ui(2560, 1600), 1280, 800, 2)
	shot, err := Prepare(c, 1024, FormatAuto)
	if err != nil || shot.Image.W != 1024 || shot.Image.H != 640 || shot.Ratio != 2.5 {
		t.Fatalf("prepared %dx%d ratio %v %v", shot.Image.W, shot.Image.H, shot.Ratio, err)
	}
}

func TestDropAlpha(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	img.SetNRGBA(0, 0, color.NRGBA{200, 100, 50, 0})
	img.SetNRGBA(1, 0, color.NRGBA{10, 20, 30, 128})
	img.SetNRGBA(2, 0, color.NRGBA{1, 2, 3, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	f, err := DecodePNG(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b := f.RGB(0, 0); r != 200 || g != 100 || b != 50 {
		t.Fatalf("a transparent pixel lost its colour: %d %d %d", r, g, b)
	}
	if r, g, b := f.RGB(1, 0); r != 10 || g != 20 || b != 30 {
		t.Fatalf("a translucent pixel was composited: %d %d %d", r, g, b)
	}
	for _, format := range []string{FormatPNG, FormatWebP, FormatJPEG} {
		e, err := Encode(f, format)
		if err != nil {
			t.Fatal(err)
		}
		switch format {
		case FormatPNG:
			if e.Data[25] != 2 {
				t.Fatalf("PNG colour type %d keeps alpha", e.Data[25])
			}
		case FormatWebP:
			if e.Data[24]&0x10 != 0 {
				t.Fatal("WebP says it uses alpha")
			}
		case FormatJPEG:
			if _, err := jpeg.Decode(bytes.NewReader(e.Data)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPhotoDetectionThreshold(t *testing.T) {
	sampled := func(colors int) *Frame {
		f := NewFrame(200, 100) // 20000 pixels, 2858 sampled
		for k := 0; k*ColorStride < f.W*f.H; k++ {
			c := 0
			if k < colors {
				c = k + 1
			}
			i := k * ColorStride * 3
			f.Pix[i], f.Pix[i+1], f.Pix[i+2] = uint8(c>>16), uint8(c>>8), uint8(c)
		}
		return f
	}
	// Colour 0 is shared by the unsampled pixels' value, so n coloured
	// samples plus black make n+1 colours.
	if Photo(sampled(PhotoColors - 1)) {
		t.Fatal("2500 distinct colours counted as a photo")
	}
	if !Photo(sampled(PhotoColors)) {
		t.Fatal("2501 distinct colours not counted as a photo")
	}
	if Photo(ui(1024, 640)) || !Photo(noise(1024, 640, 4)) {
		t.Fatal("UI or noise misclassified")
	}
}

func TestShrinkFlatWebPNoiseJPEG(t *testing.T) {
	flat := desktopCapture(t, ui(2560, 1600), 1280, 800, 2)
	shot, err := Prepare(flat, 1024, FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	if shot.Image.Format != FormatWebP {
		t.Fatalf("flat UI went out as %s", shot.Image.Format)
	}
	img, err := webp.Decode(bytes.NewReader(shot.Image.Data))
	if err != nil {
		t.Fatal(err)
	}
	sameFrame(t, FrameOf(img), shot.Frame)
	photo := desktopCapture(t, noise(1600, 1000, 5), 1600, 1000, 1)
	if shot, err = Prepare(photo, 1024, FormatAuto); err != nil || shot.Image.Format != FormatJPEG {
		t.Fatalf("noise went out as %s %v", shot.Image.Format, err)
	}
	// A capture that is not resized keeps the driver's PNG unless the
	// re-encoding is smaller.
	small := ui(400, 300)
	loose := CaptureOf(small)
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&buf, small.Image()); err != nil {
		t.Fatal(err)
	}
	loose.PNG = buf.Bytes()
	if shot, _ = Prepare(loose, 1024, FormatAuto); shot.Image.Format != FormatWebP || len(shot.Image.Data) >= len(loose.PNG) {
		t.Fatalf("a bigger driver PNG was kept (%s)", shot.Image.Format)
	}
	tight := CaptureOf(small)
	tight.PNG = []byte("\x89PNG\r\n\x1a\n tiny stand-in")
	if shot, _ = Prepare(tight, 1024, FormatAuto); shot.Image.Format != FormatPNG || !bytes.Equal(shot.Image.Data, tight.PNG) {
		t.Fatalf("a smaller driver PNG was not passed through (%s)", shot.Image.Format)
	}
}

func TestScreenshotWeightReport(t *testing.T) {
	data := mustPNG(t, solid(1024, 640, 0, 0, 0))
	got := Weigh(data)
	kb := (len(data) + 1023) / 1024
	want := "1024x640 png, " + strconv.Itoa(kb) + " KB, about 874 input tokens"
	if got != want {
		t.Fatalf("%q, want %q", got, want)
	}
	j, _ := Encode(noise(100, 75, 6), FormatJPEG)
	if w := Weigh(j.Data); !strings.HasPrefix(w, "100x75 jpeg, ") || !strings.HasSuffix(w, "about 10 input tokens") {
		t.Fatalf("jpeg: %q", w)
	}
	wp, _ := Encode(ui(30, 25), FormatWebP)
	if w := Weigh(wp.Data); !strings.HasPrefix(w, "30x25 webp, 1 KB") {
		t.Fatalf("webp: %q", w)
	}
	if w := Weigh(make([]byte, 2049)); w != "3 KB" {
		t.Fatalf("unparsable: %q", w)
	}
}

func TestJPEGOnRequest(t *testing.T) {
	f := ui(320, 200)
	j, err := Encode(f, FormatJPEG)
	if err != nil || j.MIME() != "image/jpeg" {
		t.Fatalf("%s %v", j.MIME(), err)
	}
	img, err := jpeg.Decode(bytes.NewReader(j.Data))
	if err != nil || img.Bounds().Dx() != 320 || img.Bounds().Dy() != 200 {
		t.Fatalf("decode: %v", err)
	}
	p, _ := Encode(f, FormatPNG)
	if p.MIME() != "image/png" || !bytes.Equal(p.Data, mustPNG(t, f)) {
		t.Fatal("the default PNG changed")
	}
	if _, err := Encode(f, "gif"); err == nil {
		t.Fatal("an unknown format was accepted")
	}
	if name, err := formatName("JPG"); err != nil || name != FormatJPEG {
		t.Fatalf("format name %q %v", name, err)
	}
}
