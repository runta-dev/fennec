package launcher

import (
	"context"
	"fennec/internal/clipboard"
	"fennec/internal/terminalstream"
	"io"
)

// sendClipboardImage runs on the ordered input goroutine. Reading or uploading
// an image never allows later typing (especially Return) to overtake the paste.
func sendClipboardImage(ctx context.Context, original []byte, window *terminalstream.CreditWindow, send func(terminalstream.Frame) error, read func(context.Context) ([]byte, error), notice func()) error {
	data, err := read(ctx)
	if err != nil {
		window.Add(len(original))
		notice()
		return nil
	}
	if len(data) == 0 {
		return send(terminalstream.Frame{Type: "input", Data: original})
	}
	window.Add(len(original))
	total := len(data)
	for len(data) > 0 {
		n := window.Take(min(len(data), terminalstream.Chunk))
		if n == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			return io.ErrClosedPipe
		}
		if err := send(terminalstream.Frame{Type: "image", Data: data[:n], Bytes: total}); err != nil {
			return err
		}
		total = 0
		data = data[n:]
	}
	return nil
}

var readClipboardImage = clipboard.ReadImage
