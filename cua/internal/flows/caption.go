package flows

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	"fennec/cua/internal/models"
)

// ringPt is the radius, in points, of the ring that marks a click.
const ringPt = 14

const captionPrompt = "This image is part of a screen; a red circle with four short ticks marks where a user clicked. " +
	"Name the control under the circle as a short phrase a person would use to find it on screen, for example: the Save button. " +
	"Answer with the phrase alone, in at most 8 words."

// Asker asks the ask model about images (models.Client in production).
type Asker interface {
	Ask(ctx context.Context, args models.AskArgs) (*models.Asked, error)
}

// ModelCaptioner names clicked controls with one ask over the crop, with
// the click ringed.
func ModelCaptioner(a Asker) Captioner {
	return func(ctx context.Context, c CaptionRequest) (string, error) {
		ringed, err := ringCrop(c)
		if err != nil {
			return "", err
		}
		prompt := captionPrompt
		if c.Role != "" && c.Role != "generic" && c.Role != "document" {
			prompt += " Its accessibility role is " + c.Role + ", and it has no name."
		}
		got, err := a.Ask(ctx, models.AskArgs{Prompt: prompt, Images: []string{ringed}})
		if err != nil {
			return "", err
		}
		return got.Text, nil
	}
}

// ringCrop draws the ring on the crop at the click.
func ringCrop(c CaptionRequest) (string, error) {
	data, err := base64.StdEncoding.DecodeString(c.Crop)
	if err != nil {
		return "", err
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	b := src.Bounds()
	img := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(img, img.Bounds(), src, b.Min, draw.Src)
	scale := c.Scale
	if scale <= 0 {
		scale = 1
	}
	cx := float64(b.Dx())/2 + c.Offset[0]*scale
	cy := float64(b.Dy())/2 + c.Offset[1]*scale
	r := ringPt * scale
	red := color.RGBA{R: 255, A: 255}
	plot := func(x, y float64) {
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				img.Set(int(math.Round(x))+dx, int(math.Round(y))+dy, red)
			}
		}
	}
	for a := 0.0; a < 2*math.Pi; a += 0.5 / r {
		plot(cx+r*math.Cos(a), cy+r*math.Sin(a))
	}
	for _, d := range [][2]float64{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		for t := r; t <= r*1.5; t++ {
			plot(cx+d[0]*t, cy+d[1]*t)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
