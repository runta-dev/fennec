package cli

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectIcon(t *testing.T) {
	path := filepath.Join(t.TempDir(), "icon.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 256, 128))
	for y := 0; y < 128; y++ {
		for x := 64; x < 192; x++ {
			img.Set(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	if err = png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	uri, err := projectIcon(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got.Bounds() != image.Rect(0, 0, 128, 128) {
		t.Fatal(got.Bounds())
	}
	r, _, _, a := got.At(0, 0).RGBA()
	if r != 65535 || a != 65535 {
		t.Fatal("icon was not center-cropped")
	}
	os.WriteFile(path, []byte("not an image"), 0600)
	if _, err = projectIcon(path); err == nil {
		t.Fatal("accepted invalid image")
	}
}
