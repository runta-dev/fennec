//go:build !cgo || ios || android

package pasteimage

import (
	"bytes"
	"image"

	"github.com/gen2brain/webp"
)

const native = false

// encode uses libwebp transpiled to Go (or a system libwebp, if purego finds
// one). Method 2 instead of 4: 2.5x faster, about 3% larger (FEN-191).
func encode(m image.Image) ([]byte, error) {
	var out bytes.Buffer
	if err := webp.Encode(&out, m, webp.Options{Quality: quality, Method: 2}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
