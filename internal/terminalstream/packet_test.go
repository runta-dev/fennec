package terminalstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type packetCapture struct {
	bytes.Buffer
	first chan struct{}
	once  sync.Once
	max   int
}

func (w *packetCapture) Write(data []byte) (int, error) {
	w.max = max(w.max, len(data))
	n, err := w.Buffer.Write(data)
	w.once.Do(func() { close(w.first) })
	return n, err
}

func TestPacketPongPassesLargeReplyAndPreservesResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := newWriteScheduler(ctx)
	s.packets.enabled.Store(true)
	out := &packetCapture{first: make(chan struct{})}
	result := json.RawMessage(`{"screen":"` + strings.Repeat("x", 96<<10) + `"}`)
	bulk := make(chan error, 1)
	go func() {
		bulk <- s.sendFrames(out, Frame{Type: "result", ID: "large-reply", Result: result}, Frame{Type: "exit", ID: "large-reply"})
	}()
	<-out.first
	started := time.Now()
	if err := s.sendFrames(out, Frame{Type: "pong", ID: "fresh-ping"}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 75*time.Millisecond {
		t.Errorf("pong blocked behind the large reply: %s", elapsed)
	}
	if err := <-bulk; err != nil {
		t.Fatal(err)
	}
	if out.max+packetOverhead > 2048 {
		t.Fatalf("oversized packet write: %d", out.max)
	}
	decoder := packetDecoder{}
	scanner := bufio.NewScanner(bytes.NewReader(out.Bytes()))
	var types []string
	for scanner.Scan() {
		frame, complete, err := decoder.decode(scanner.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if !complete {
			continue
		}
		types = append(types, frame.Type)
		if frame.Type == "result" && (!bytes.Equal(frame.Result, result) || frame.Bytes != 0 || frame.Seq != 0 || frame.Data != nil) {
			t.Fatal("large reply changed or inherited fragment metadata")
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(types) != "[pong result exit]" {
		t.Fatalf("wrong completion order: %v", types)
	}
	if decoder.reserved != 0 || len(decoder.pending) != 0 {
		t.Fatal("completed reply retained its assembly budget")
	}
}

func TestFragmentDecoderRejectsInvalidSequencesAndBudgets(t *testing.T) {
	for name, frames := range map[string][]Frame{
		"missing start": {{Type: "fragment", ID: "1", Seq: 1, Bytes: 10, Data: []byte("a")}},
		"empty":         {{Type: "fragment", ID: "1", Bytes: 10}},
		"too large":     {{Type: "fragment", ID: "1", Bytes: packetMessageMax + 1, Data: []byte("a")}},
		"overrun":       {{Type: "fragment", ID: "1", Bytes: 1, Data: []byte("aa")}},
		"duplicate":     {{Type: "fragment", ID: "1", Bytes: 10, Data: []byte("a")}, {Type: "fragment", ID: "1", Bytes: 10, Data: []byte("a")}},
		"size changed":  {{Type: "fragment", ID: "1", Bytes: 10, Data: []byte("a")}, {Type: "fragment", ID: "1", Seq: 1, Bytes: 11, Data: []byte("a")}},
		"aggregate":     {{Type: "fragment", ID: "1", Bytes: packetMessageMax, Data: []byte("a")}, {Type: "fragment", ID: "2", Bytes: packetMessageMax, Data: []byte("a")}},
	} {
		t.Run(name, func(t *testing.T) {
			decoder := packetDecoder{}
			var err error
			for _, frame := range frames {
				raw, _ := json.Marshal(frame)
				if _, _, err = decoder.decode(raw); err != nil {
					break
				}
			}
			if err == nil {
				t.Fatal("invalid fragment accepted")
			}
		})
	}
}

type fragmentObserver struct {
	io.WriteCloser
	started chan struct{}
	once    sync.Once
}

func (w *fragmentObserver) Write(data []byte) (int, error) {
	n, err := w.WriteCloser.Write(data)
	if bytes.Contains(data, []byte(`"type":"fragment"`)) {
		w.once.Do(func() { close(w.started) })
	}
	return n, err
}

func TestResidentNegotiatesPacketsAndAnswersFreshPingDuringLargeOperation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	observed := &fragmentObserver{WriteCloser: output, started: make(chan struct{})}
	result := json.RawMessage(`{"screen":"` + strings.Repeat("x", 96<<10) + `"}`)
	server := make(chan error, 1)
	go func() {
		server <- ServeWithOperations(ctx, input, observed, nil, nil, nil, func(context.Context, cloudruntime.LocalOperation) (json.RawMessage, error) { return result, nil })
	}()
	transport := StartPipes(send, receive)
	defer transport.Close()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if !transport.Supports(CapabilityTrafficPackets) {
		t.Fatal("packet scheduling capability missing")
	}
	operation := make(chan error, 1)
	go func() {
		got, err := transport.Operate(ctx, cloudruntime.LocalOperation{Kind: "list"})
		if err == nil && !bytes.Equal(got, result) {
			err = fmt.Errorf("operation result changed")
		}
		operation <- err
	}()
	select {
	case <-observed.started:
	case <-ctx.Done():
		t.Fatal("operation never started sending fragments")
	}
	start := time.Now()
	if err := transport.probe(ctx, false); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 75*time.Millisecond {
		t.Errorf("fresh protocol ping waited for large operation: %s", elapsed)
	}
	if err := <-operation; err != nil {
		t.Fatal(err)
	}
	transport.Close()
	cancel()
	select {
	case <-server:
	case <-time.After(time.Second):
		t.Fatal("packet server did not stop")
	}
}
