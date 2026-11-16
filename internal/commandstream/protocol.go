// Package commandstream carries non-PTY commands over a paced resident byte
// stream. It has no dependency on application services or SSH carrier workers.
package commandstream

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

const (
	request byte = iota + 1
	stdinData
	stdinEOF
	stdoutData
	stderrData
	exitStatus
)
const maxRecord = 128 << 10
const chunk = 1024

type records struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *records) send(kind byte, data []byte) error {
	if len(data) > maxRecord {
		return fmt.Errorf("command record too large")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var header [5]byte
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	if err := writeAll(w.out, header[:]); err != nil {
		return err
	}
	return writeAll(w.out, data)
}
func writeAll(out io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := out.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
func readRecord(in io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(in, header[:]); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(header[1:])
	if size > maxRecord {
		return 0, nil, fmt.Errorf("command record too large")
	}
	data := make([]byte, int(size))
	_, err := io.ReadFull(in, data)
	return header[0], data, err
}

type outputWriter struct {
	records *records
	kind    byte
}

func (w outputWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := min(len(p), chunk)
		if err := w.records.send(w.kind, p[:n]); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}
