package predict

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Recorder writes a session's server output and typed input with timestamps,
// in the layout the trace tests replay: "output" holds raw bytes, "timing"
// holds "<seconds since previous chunk> <bytes>" per chunk (script's
// --log-timing format), "input" holds "<seconds since start> <hex>" per key
// packet, and "acks" holds the cloud's echo acknowledgements. It is opt-in, local and private (0700/0600); recordings can
// contain anything shown on screen.
type Recorder struct {
	mu                          sync.Mutex
	output, timing, input, acks *os.File
	start, last                 time.Time
}

// NewRecorder creates dir/<name>-<time> and its files.
func NewRecorder(dir, name string) (*Recorder, error) {
	path := filepath.Join(dir, fmt.Sprintf("%s-%s", name, time.Now().UTC().Format("20060102T150405")))
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	open := func(file string) (*os.File, error) {
		return os.OpenFile(filepath.Join(path, file), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	}
	r := &Recorder{start: time.Now()}
	r.last = r.start
	var err error
	if r.output, err = open("output"); err != nil {
		return nil, err
	}
	if r.timing, err = open("timing"); err != nil {
		r.output.Close()
		return nil, err
	}
	if r.acks, err = open("acks"); err != nil {
		r.output.Close()
		r.timing.Close()
		return nil, err
	}
	if r.input, err = open("input"); err != nil {
		r.acks.Close()
		r.output.Close()
		r.timing.Close()
		return nil, err
	}
	return r, nil
}

// Output records server bytes as they reach the terminal.
func (r *Recorder) Output(data []byte) {
	if r == nil || len(data) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.output.Write(data)
	fmt.Fprintf(r.timing, "%.6f %d\n", now.Sub(r.last).Seconds(), len(data))
	r.last = now
}

// Input records one packet of decoded keys.
func (r *Recorder) Input(data []byte) {
	if r == nil || len(data) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.input, "%.6f %s\n", time.Since(r.start).Seconds(), hex.EncodeToString(data))
}

// Ack records an echo acknowledgement: "<seconds since start> <seq> <quiet>".
func (r *Recorder) Ack(seq uint64, quiet bool) {
	if r == nil || seq == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.acks, "%.6f %d %v\n", time.Since(r.start).Seconds(), seq, quiet)
}

// Close flushes and closes the files.
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.output.Close()
	r.timing.Close()
	r.input.Close()
	r.acks.Close()
}
