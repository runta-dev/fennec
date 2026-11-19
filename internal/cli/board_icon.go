package cli

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
)

// projectIcon normalizes a picker-selected image to the board's small PNG icon.
func projectIcon(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 16<<20+1))
	if err != nil || len(data) > 16<<20 {
		return "", fmt.Errorf("image is too large")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return "", fmt.Errorf("unsupported image")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	left, top := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	dst := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			dst.Set(x, y, src.At(left+x*side/128, top+y*side/128))
		}
	}
	var out bytes.Buffer
	if err = png.Encode(&out, dst); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(out.Bytes()), nil
}
