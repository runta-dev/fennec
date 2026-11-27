package pasteimage

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
)

func TestCompressionKeepsDimensionsAlphaAndDoesNotReencode(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			im.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), 120, uint8(x)})
		}
	}
	var input bytes.Buffer
	if err := png.Encode(&input, im); err != nil {
		t.Fatal(err)
	}
	compressed, err := Compress(input.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	decoded, format, err := image.Decode(bytes.NewReader(compressed))
	if err != nil || format != "webp" || decoded.Bounds() != im.Bounds() {
		t.Fatalf("invalid WebP: %s %v", format, err)
	}
	_, _, _, alpha := decoded.At(80, 80).RGBA()
	if alpha != 80*257 {
		t.Fatalf("alpha changed: %d", alpha)
	}
	again, err := Compress(compressed)
	if err != nil || !bytes.Equal(again, compressed) {
		t.Fatal("recompressed WebP", err)
	}
	compat, err := PNG(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(compat)); err != nil {
		t.Fatal(err)
	}
	if _, err := Compress([]byte("invalid")); err == nil {
		t.Fatal("accepted invalid image")
	}
	path := t.TempDir() + "/input.png"
	if err := os.WriteFile(path, input.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := File(path)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(out)
	info, err := os.Stat(out)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("image is not private", err)
	}
}

// A premultiplied, translucent RGBA image must come out with the same
// straight colors, not darkened by its alpha (the native encoder wants NRGBA).
func TestCompressionUnpremultipliesRGBA(t *testing.T) {
	im := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			im.Set(x, y, color.NRGBA{200, 100, 50, 128})
		}
	}
	compressed, decoded, err := CompressImage(mustPNG(t, im))
	if err != nil || decoded == nil {
		t.Fatal(err)
	}
	out, _, err := image.Decode(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	c := color.NRGBAModel.Convert(out.At(32, 32)).(color.NRGBA)
	if c.A != 128 || absDiff(c.R, 200) > 20 || absDiff(c.G, 100) > 20 || absDiff(c.B, 50) > 20 {
		t.Fatalf("color changed: %+v (native %v)", c, Native())
	}
}

func mustPNG(t *testing.T, im image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func absDiff(a, b uint8) uint8 {
	if a > b {
		return a - b
	}
	return b - a
}
