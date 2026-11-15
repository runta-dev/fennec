package launcher

import (
	"context"
	"fennec/internal/clipboard"
	"fennec/internal/terminalstream"
	"io"
)

// sendClipboardImage runs on the ordered input goroutine. Reading or uploading
// an image never allows later typing (especially Return) to overtake the paste.
// seq is the input sequence number of the shortcut; every chunk carries it so
// the service can acknowledge the upload once the paste is written. absent is
// called when the clipboard turned out to hold no image.
func sendClipboardImage(ctx context.Context, original []byte, seq uint64, window *terminalstream.CreditWindow, send func(terminalstream.Frame) error, read func(context.Context) ([]byte, error), notice func(), absent func()) error {
	data, err := read(ctx)
	if err != nil {
		window.Add(len(original))
		notice()
		return nil
	}
	if len(data) == 0 {
		if absent != nil {
			absent()
		}
		return send(terminalstream.Frame{Type: "input", Data: original, Seq: seq})
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
		if err := send(terminalstream.Frame{Type: "image", Data: data[:n], Bytes: total, Seq: seq}); err != nil {
			return err
		}
		total = 0
		data = data[n:]
	}
	return nil
}

var readClipboardImage = clipboard.ReadImage
