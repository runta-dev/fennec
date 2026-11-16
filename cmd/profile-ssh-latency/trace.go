package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/traffic"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Trace only this probe's small, uniquely matched ping frames. No payload,
// command or credential content is retained. All intervals use the server's
// monotonic clock, and the wire encoding and broker route remain unchanged.
type serverPingTiming struct {
	ID         string  `json:"id"`
	DispatchMS float64 `json:"dispatch_ms"`
	WriteMS    float64 `json:"write_ms"`
	Failed     bool    `json:"failed,omitempty"`
}

type pingTrace struct {
	mu      sync.Mutex
	pending map[string]time.Time
	samples []serverPingTiming
	dropped int
	file    *os.File
}

func openPingTrace(path string) (*pingTrace, error) {
	if !safePath(path) {
		return nil, fmt.Errorf("ping trace requires a safe absolute path")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	return &pingTrace{file: f, pending: make(map[string]time.Time)}, nil
}

func (t *pingTrace) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	err := json.NewEncoder(t.file).Encode(struct {
		Boundary string             `json:"boundary"`
		Samples  []serverPingTiming `json:"samples"`
		Dropped  int                `json:"dropped"`
		Pending  int                `json:"pending"`
	}{"Dedicated probe only. Dispatch runs from receipt of the complete ping line to broker submission; write runs until broker completion. Intervals include local scheduling. Broker completion means a write into sshd's pipe, not delivery over the network. Match client IDs; do not subtract independent percentiles or cross-host timestamps.", t.samples, t.dropped, len(t.pending)})
	if closeErr := t.file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func pingID(line []byte, kind string) string {
	if !bytes.Contains(line, []byte(`"type":"`+kind+`"`)) {
		return ""
	}
	var frame struct{ Type, ID string }
	if json.Unmarshal(line, &frame) != nil || frame.Type != kind ||
		!strings.HasPrefix(frame.ID, "measure-") || len(frame.ID) > 64 {
		return ""
	}
	return frame.ID
}

type traceInput struct {
	in      io.ReadCloser
	trace   *pingTrace
	partial []byte
	skipped bool
}

func (r *traceInput) Close() error { return r.in.Close() }

func (r *traceInput) Read(p []byte) (int, error) {
	n, err := r.in.Read(p)
	r.observe(p[:n], time.Now())
	return n, err
}

func (r *traceInput) observe(data []byte, received time.Time) {
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		length := len(data)
		if end >= 0 {
			length = end
		}
		if len(r.partial)+length > 1024 {
			r.skipped = true
			r.partial = r.partial[:0]
		}
		if !r.skipped {
			r.partial = append(r.partial, data[:length]...)
		}
		if end < 0 {
			return
		}
		if !r.skipped {
			if id := pingID(r.partial, "ping"); id != "" {
				r.trace.mu.Lock()
				if len(r.trace.pending) < 32 {
					r.trace.pending[id] = received
				} else {
					r.trace.dropped++
				}
				r.trace.mu.Unlock()
			}
		}
		r.partial = r.partial[:0]
		r.skipped = false
		data = data[end+1:]
	}
}

type packetWriter interface {
	io.Writer
	WritePacket(context.Context, string, traffic.Class, []byte) error
}

type traceOutput struct {
	packetWriter
	trace *pingTrace
}

func (w traceOutput) Close() error {
	if closer, ok := w.packetWriter.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (w traceOutput) WritePacket(ctx context.Context, flow string, class traffic.Class, data []byte) error {
	id := pingID(data, "pong")
	if id == "" {
		return w.packetWriter.WritePacket(ctx, flow, class, data)
	}
	started := time.Now()
	err := w.packetWriter.WritePacket(ctx, flow, class, data)
	ended := time.Now()
	w.trace.mu.Lock()
	defer w.trace.mu.Unlock()
	if received, ok := w.trace.pending[id]; ok {
		delete(w.trace.pending, id)
		if len(w.trace.samples) < 4096 {
			w.trace.samples = append(w.trace.samples, serverPingTiming{ID: id, DispatchMS: ms(started.Sub(received)), WriteMS: ms(ended.Sub(started)), Failed: err != nil})
		} else {
			w.trace.dropped++
		}
	}
	return err
}
