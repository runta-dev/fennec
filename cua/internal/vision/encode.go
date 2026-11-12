package vision

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image/jpeg"
	"strings"
)

// Encoding.
const (
	// JPEGQuality is the quality of photo-like screenshots.
	JPEGQuality = 80
	// PhotoColors is how many distinct colours of the sample make an image
	// photo-like.
	PhotoColors = 2500
	// ColorStride samples every 7th pixel when counting colours.
	ColorStride = 7
	// PixelsPerToken estimates a model's input tokens for an image.
	PixelsPerToken = 750
)

// Image formats.
const (
	FormatAuto = ""     // lossless WebP, or JPEG for photo-like content
	FormatPNG  = "png"  // the default of window and screen captures
	FormatJPEG = "jpeg" // on request
	FormatWebP = "webp"
)

// Encoded is an encoded image.
type Encoded struct {
	Data   []byte
	Format string // png, jpeg, webp
	W, H   int
}

// MIME is the image's media type.
func (e Encoded) MIME() string { return "image/" + e.Format }

// Base64 is the image as base64.
func (e Encoded) Base64() string { return base64.StdEncoding.EncodeToString(e.Data) }

// Photo says whether every ColorStride-th pixel shows more than PhotoColors
// distinct colours: photos and video compress better lossy, UI lossless.
func Photo(f *Frame) bool {
	seen := make(map[uint32]struct{}, PhotoColors+1)
	for i := 0; i+2 < len(f.Pix); i += 3 * ColorStride {
		seen[uint32(f.Pix[i])<<16|uint32(f.Pix[i+1])<<8|uint32(f.Pix[i+2])] = struct{}{}
		if len(seen) > PhotoColors {
			return true
		}
	}
	return false
}

// Encode writes the frame in format: FormatAuto picks JPEG at JPEGQuality
// for photo-like content and lossless WebP otherwise. The alpha channel is
// never written.
func Encode(f *Frame, format string) (Encoded, error) {
	if format == FormatAuto {
		format = FormatWebP
		if Photo(f) {
			format = FormatJPEG
		}
	}
	out := Encoded{Format: format, W: f.W, H: f.H}
	var err error
	switch format {
	case FormatPNG:
		out.Data, err = EncodePNG(f)
	case FormatWebP:
		out.Data, err = EncodeWebP(f)
	case FormatJPEG:
		var buf bytes.Buffer
		err = jpeg.Encode(&buf, f.Image(), &jpeg.Options{Quality: JPEGQuality})
		out.Data = buf.Bytes()
	default:
		return Encoded{}, fmt.Errorf("image format %q is not png, jpeg or webp", format)
	}
	if err != nil {
		return Encoded{}, err
	}
	return out, nil
}

// ImageSize reads the format and pixel size from a PNG, WebP or JPEG header.
func ImageSize(data []byte) (format string, w, h int, ok bool) {
	be16 := func(i int) (int, bool) {
		if i+2 > len(data) {
			return 0, false
		}
		return int(binary.BigEndian.Uint16(data[i:])), true
	}
	switch {
	case len(data) >= 24 && bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return FormatPNG, int(binary.BigEndian.Uint32(data[16:])), int(binary.BigEndian.Uint32(data[20:])), true
	case len(data) >= 25 && string(data[0:4]) == "RIFF" && string(data[8:16]) == "WEBPVP8L":
		bits := binary.LittleEndian.Uint32(data[21:])
		return FormatWebP, int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1, true
	case len(data) >= 4 && data[0] == 0xff && data[1] == 0xd8:
		for i := 2; i+1 < len(data) && data[i] == 0xff; {
			marker := data[i+1]
			if marker >= 0xc0 && marker <= 0xc3 {
				hh, ok1 := be16(i + 5)
				ww, ok2 := be16(i + 7)
				return FormatJPEG, ww, hh, ok1 && ok2
			}
			n, ok := be16(i + 2)
			if !ok {
				break
			}
			i += 2 + n
		}
	}
	return "", 0, 0, false
}

// Weigh describes an image for the agent: its format, pixel size, size in
// KB rounded up and an estimate of the input tokens it costs; only the KB
// size when the image cannot be parsed.
func Weigh(data []byte) string {
	kb := (len(data) + 1023) / 1024
	format, w, h, ok := ImageSize(data)
	if !ok {
		return fmt.Sprintf("%d KB", kb)
	}
	tokens := (w*h + PixelsPerToken - 1) / PixelsPerToken
	return fmt.Sprintf("%dx%d %s, %d KB, about %d input tokens", w, h, format, kb, tokens)
}

// Screenshot is a capture prepared for an agent: shrunk to its long edge,
// encoded, and the shrink ratio (capture pixels per image pixel).
type Screenshot struct {
	Image Encoded
	// Ratio is capture pixels per screenshot pixel (1 when not resized).
	Ratio float64
	// Frame is the screenshot's pixels.
	Frame *Frame
}

// Prepare shrinks a capture so its long edge is maxEdge (0 keeps the full
// size) with Lanczos3 and encodes it in format. With FormatAuto, a capture
// that is not resized keeps the driver's PNG unless the re-encoding is
// smaller.
func Prepare(c *Capture, maxEdge int, format string) (Screenshot, error) {
	f, err := c.Pixels()
	if err != nil {
		return Screenshot{}, err
	}
	small := Lanczos(f, maxEdge)
	shot := Screenshot{Frame: small, Ratio: float64(f.W) / float64(small.W)}
	if shot.Image, err = Encode(small, format); err != nil {
		return Screenshot{}, err
	}
	if small == f && format == FormatAuto && len(c.PNG) > 0 && len(c.PNG) <= len(shot.Image.Data) {
		shot.Image = Encoded{Data: c.PNG, Format: FormatPNG, W: f.W, H: f.H}
	}
	return shot, nil
}

// formatName normalizes a requested format.
func formatName(s string) (string, error) {
	switch strings.ToLower(s) {
	case "", "auto":
		return FormatAuto, nil
	case "png":
		return FormatPNG, nil
	case "jpeg", "jpg":
		return FormatJPEG, nil
	case "webp":
		return FormatWebP, nil
	}
	return "", fmt.Errorf("image format %q is not png, jpeg or webp", s)
}
