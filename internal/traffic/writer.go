package traffic

import (
	"context"
	"io"
)

// Writer admits small writes to one independent transport stream. Different
// Writers may share a Scheduler, but they must not write fragments into the
// same framed byte stream without a framing layer that supports interleaving.
// Overhead includes SSH/TCP framing at the integration point; it is charged
// for every packet, not once per application Write.
type Writer struct {
	Scheduler *Scheduler
	Context   context.Context
	Flow      string
	Class     Class
	Output    io.Writer
	Chunk     int
	Overhead  int
}

func (w Writer) Write(data []byte) (int, error) {
	if w.Chunk <= 0 || w.Overhead < 0 || w.Chunk+w.Overhead > w.Scheduler.config.MaxPacketBytes {
		return 0, ErrPacketSize
	}
	written := 0
	for len(data) > 0 {
		chunk := data[:min(len(data), w.Chunk)]
		n := 0
		err := w.Scheduler.Write(w.Context, w.Flow, w.Class, len(chunk)+w.Overhead, func() error {
			var err error
			n, err = w.Output.Write(chunk)
			if err == nil && n != len(chunk) {
				err = io.ErrShortWrite
			}
			return err
		})
		written += n
		if err != nil {
			return written, err
		}
		data = data[n:]
	}
	return written, nil
}
