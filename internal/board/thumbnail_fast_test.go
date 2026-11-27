package board

import (
	"image"
	pixelcolor "image/color"
	"testing"
)

// opaqueImage hides the concrete type, so thumbnail takes the generic path.
type opaqueImage struct{ image.Image }

func TestThumbnailFastPathsMatchGenericPath(t *testing.T) {
	nrgba := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	rgba := image.NewRGBA(image.Rect(0, 0, 300, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 300; x++ {
			nrgba.SetNRGBA(x, y, pixelcolor.NRGBA{uint8(x), uint8(y), uint8(x ^ y), uint8(255 - x/2)})
			rgba.SetRGBA(x, y, pixelcolor.RGBA{uint8(y), uint8(x), uint8(x + y), 255})
		}
	}
	for name, im := range map[string]image.Image{"nrgba": nrgba, "rgba": rgba} {
		fast, err := thumbnail(im)
		if err != nil {
			t.Fatal(err)
		}
		slow, err := thumbnail(opaqueImage{im})
		if err != nil {
			t.Fatal(err)
		}
		if fast != slow {
			t.Fatalf("%s: fast thumbnail differs from the generic one", name)
		}
	}
}
