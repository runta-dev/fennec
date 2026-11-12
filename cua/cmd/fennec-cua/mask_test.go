package main

import (
	"bytes"
	"encoding/base64"
	"image/color"
	"image/png"
	"testing"

	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// Under an allowlist a screen capture shows only admitted apps' windows; an
// accessory app's floating window on top of an admitted one is blacked out
// (found live: the fixture's CUACover cover showed in a screen capture).
func TestMaskImage(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, policy.SolidImage(100, 100, color.White)); err != nil {
		t.Fatal(err)
	}
	im := &protocol.Image{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(buf.Bytes()), Width: 100, Height: 100, Scale: 1}
	ws := []view.RawWindow{
		{App: "CUACover", Bounds: &[4]int{40, 40, 20, 20}},
		{App: "CUAFixture", Bounds: &[4]int{0, 0, 80, 80}},
	}
	if err := maskImage(im, policy.NewAllowlist([]string{"CUAFixture"}), ws); err != nil {
		t.Fatal(err)
	}
	data, _ := base64.StdEncoding.DecodeString(im.Data)
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	dark := func(x, y int) bool { r, _, _, _ := img.At(x, y).RGBA(); return r == 0 }
	if dark(10, 10) || !dark(50, 50) || !dark(90, 90) {
		t.Fatalf("admitted %v, cover %v, desktop %v", !dark(10, 10), dark(50, 50), dark(90, 90))
	}
}
