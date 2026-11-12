package mcpserver

import (
	"bytes"
	"encoding/base64"
	"strings"
)

// SniffImage names the image format of data from its leading bytes: its
// MIME type and file extension, or empty strings when it is not a PNG, JPEG
// or WebP image.
func SniffImage(data []byte) (mime, ext string) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", "png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg", "jpg"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", "webp"
	}
	return "", ""
}

// sniffBase64 sniffs the start of base64 image data.
func sniffBase64(data string) string {
	head := data
	if len(head) > 24 {
		head = head[:24] // 18 bytes, enough for every signature
	}
	raw, err := base64.StdEncoding.DecodeString(head)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(head, "="))
		if err != nil {
			return ""
		}
	}
	mime, _ := SniffImage(raw)
	return mime
}
