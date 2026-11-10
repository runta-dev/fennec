package runtime

import (
	"bytes"
	"fennec/internal/terminalnotice"
	"io"
	"strings"
)

// Replace only Herdr's complete, address-specific detach trailer. Keep PTY
// control sequences and arbitrary output intact, including split writes.
type detachOutput struct {
	output      io.Writer
	address, id string
	pending     []byte
	styled      bool
}

func (w *detachOutput) Write(data []byte) (int, error) {
	size := len(data)
	w.pending = append(w.pending, data...)
	marker := []byte("\r\n[detached from " + w.address + "]\r\n")
	for {
		if at := bytes.Index(w.pending, marker); at >= 0 {
			if _, err := w.output.Write(w.pending[:at]); err != nil {
				return 0, err
			}
			message := strings.ReplaceAll(terminalnotice.Detached(w.id, w.styled), "\n", "\r\n")
			if w.styled {
				message = "\x1b[2K" + strings.ReplaceAll(message, "\r\n", "\r\n\x1b[2K")
			}
			if _, err := io.WriteString(w.output, "\r\n\r\n"+message+"\r\n"); err != nil {
				return 0, err
			}
			w.pending = w.pending[at+len(marker):]
			continue
		}
		keep := 0
		for n := 1; n < len(marker) && n <= len(w.pending); n++ {
			if bytes.Equal(w.pending[len(w.pending)-n:], marker[:n]) {
				keep = n
			}
		}
		if _, err := w.output.Write(w.pending[:len(w.pending)-keep]); err != nil {
			return 0, err
		}
		w.pending = append([]byte(nil), w.pending[len(w.pending)-keep:]...)
		return size, nil
	}
}
func (w *detachOutput) Flush() {
	if len(w.pending) > 0 {
		_, _ = w.output.Write(w.pending)
		w.pending = nil
	}
}
