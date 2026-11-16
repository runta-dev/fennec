package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/traffic"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type tracePacketSink struct {
	bytes.Buffer
	err   error
	class traffic.Class
	flow  string
}

func (w *tracePacketSink) WritePacket(_ context.Context, flow string, class traffic.Class, p []byte) error {
	w.class, w.flow = class, flow
	w.Write(p)
	return w.err
}

func TestPingTraceHandlesSplitLinesAndPreservesWire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.json")
	trace, err := openPingTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openPingTrace(path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrote an existing trace: %v", err)
	}
	data := append(bytes.Repeat([]byte("x"), 4096), '\n')
	data = append(data, []byte("{\"type\":\"ping\",\"id\":\"measure-9\"}\n")...)
	r := &traceInput{in: io.NopCloser(bytes.NewReader(data)), trace: trace}
	var got bytes.Buffer
	chunk := make([]byte, 7)
	for {
		n, err := r.Read(chunk)
		got.Write(chunk[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(data, got.Bytes()) || len(trace.pending) != 1 {
		t.Fatalf("reader changed bytes or lost split ping: pending=%d", len(trace.pending))
	}
	// A known pause before broker submission must be attributed to dispatch,
	// without imposing a sleep on the test or inventing a remote clock offset.
	trace.pending["measure-9"] = time.Now().Add(-50 * time.Millisecond)
	sink := &tracePacketSink{err: io.ErrClosedPipe}
	w := traceOutput{packetWriter: sink, trace: trace}
	pong := []byte("{\"type\":\"pong\",\"id\":\"measure-9\"}\n")
	if err := w.WritePacket(context.Background(), "measure-9", traffic.Interactive, pong); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if !bytes.Equal(sink.Bytes(), pong) || sink.class != traffic.Interactive || sink.flow != "measure-9" {
		t.Fatal("observer changed broker routing or bytes")
	}
	if err := trace.close(); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Samples          []serverPingTiming
		Pending, Dropped int
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Samples) != 1 || result.Pending != 0 || result.Dropped != 0 ||
		result.Samples[0].DispatchMS < 50 || !result.Samples[0].Failed || result.Samples[0].ID != "measure-9" {
		t.Fatalf("incorrect matched intervals: %+v", result)
	}
	timing := result.Samples[0]
	if timing.Received.IsZero() || timing.Submitted.Before(timing.Received) || timing.Written.Before(timing.Submitted) {
		t.Fatalf("missing or reordered same-host timestamps: %+v", timing)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private trace permissions: %v %v", info, err)
	}
}

func TestTraceOnlyRecordsValidSSHTuple(t *testing.T) {
	for _, value := range []string{"secret", "10.0.2.1 0 10.0.2.3 22", "10.0.2.1 42 host 22", "10.0.2.1 42 10.0.2.3 65536"} {
		t.Setenv("SSH_CONNECTION", value)
		if traceSSHConnection() != "" {
			t.Fatal("unexpected environment data recorded")
		}
	}
	t.Setenv("SSH_CONNECTION", "10.0.2.1 4242 10.0.2.3 22")
	if traceSSHConnection() != "10.0.2.1 4242 10.0.2.3 22" {
		t.Fatal("valid socket identity lost")
	}
}

func TestProbePathsRejectShellSyntax(t *testing.T) {
	for _, path := range []string{"relative", "/tmp/x&cmd", "/tmp/x|cmd", "/tmp/x>out", "/tmp/$(cmd)", "/tmp/*", "/tmp/x;cmd"} {
		if safePath(path) {
			t.Errorf("accepted command syntax: %q", path)
		}
	}
	if !safePath("/tmp/fennec-ping_trace.1.json") {
		t.Fatal("rejected staged path")
	}
}
