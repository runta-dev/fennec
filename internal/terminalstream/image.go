package terminalstream

import (
	"bytes"
	"fennec/internal/clipboard"
	"fmt"
	"os"
	"strings"
)

const CapabilityImagePaste = "image_paste_v1"

// An image uses the same ordered, credited input queue as keystrokes. Bytes
// announces the total size on the first chunk; subsequent chunks carry zero.
// Only a complete, validated image becomes an agent-visible path.
type imageUpload struct {
	data bytes.Buffer
	size int
}

func (u *imageUpload) reset() { u.data = bytes.Buffer{}; u.size = 0 }

func (u *imageUpload) accept(frame Frame) (string, error) {
	if frame.Bytes != 0 {
		if u.size != 0 || frame.Bytes < 1 || frame.Bytes > clipboard.MaxImageBytes {
			u.reset()
			return "", fmt.Errorf("invalid image upload size")
		}
		u.size = frame.Bytes
	}
	if u.size == 0 || len(frame.Data) == 0 || u.data.Len()+len(frame.Data) > u.size {
		u.reset()
		return "", fmt.Errorf("invalid image upload")
	}
	u.data.Write(frame.Data)
	if u.data.Len() < u.size {
		return "", nil
	}
	defer u.reset()
	if err := clipboard.ValidatePNG(u.data.Bytes()); err != nil {
		return "", err
	}
	file, err := os.CreateTemp("", "fennec-pasted-*.png")
	if err != nil {
		return "", err
	}
	path := file.Name()
	_, writeErr := file.Write(u.data.Bytes())
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || strings.ContainsAny(path, "\x00\x1b\r\n") {
		os.Remove(path)
		return "", fmt.Errorf("could not save pasted image")
	}
	// Retain the attachment after detach: the agent may not read it until the
	// user submits their draft, or may refer to it again after a reconnect.
	return path, nil
}

func imagePaste(path string) []byte {
	return []byte("\x1b[200~" + path + "\x1b[201~")
}
