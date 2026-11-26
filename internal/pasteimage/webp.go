// Package pasteimage normalizes pasted images before network transfer.
package pasteimage

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"syscall"

	"github.com/gen2brain/webp"
)

const MaxBytes = 64 << 20

// File creates a private compressed copy; the caller owns its lifetime.
func File(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("invalid image file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return "", err
	}
	data, err = Compress(data)
	if err != nil {
		return "", err
	}
	out, err := os.CreateTemp("", "fennec-pasted-*.webp")
	if err != nil {
		return "", err
	}
	_, writeErr := out.Write(data)
	closeErr := out.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(out.Name())
		return "", fmt.Errorf("could not save image")
	}
	return out.Name(), nil
}

// Compress preserves pixel dimensions and alpha at quality 85. It never drops quality
// to chase a size target: text legibility takes precedence over the 75% goal.
// Already encoded WebP is retained to avoid repeated lossy generations.
func Compress(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > MaxBytes {
		return nil, fmt.Errorf("invalid image size")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 16384 || cfg.Height > 16384 || int64(cfg.Width)*int64(cfg.Height) > 32_000_000 {
		return nil, fmt.Errorf("invalid image dimensions")
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if format == "webp" {
		return data, nil
	}
	var out bytes.Buffer
	if err := webp.Encode(&out, decoded, webp.Options{Quality: 85, Method: 4}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// PNG adapts the compressed wire representation to agents' PNG clipboard API.
func PNG(data []byte) ([]byte, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return data, nil
	}
	if _, err := Compress(data); err != nil {
		return nil, err
	}
	im, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err = png.Encode(&out, im)
	return out.Bytes(), err
}
