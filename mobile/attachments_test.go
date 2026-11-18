package fennecmobile

import (
	"context"
	"errors"
	"fennec/internal/terminalstream"
	"strings"
	"sync"
	"testing"
	"time"
)

type nativeImageFixture struct {
	frames    chan terminalstream.Frame
	mu        sync.Mutex
	sent      []terminalstream.Frame
	remaining int
	reject    bool
}

func (f *nativeImageFixture) Next(ctx context.Context) (terminalstream.Frame, error) {
	select {
	case frame := <-f.frames:
		return frame, nil
	case <-ctx.Done():
		return terminalstream.Frame{}, ctx.Err()
	}
}
func (f *nativeImageFixture) Send(frame terminalstream.Frame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, frame)
	if frame.Type == "credit" {
		return nil
	}
	if frame.Type == "image" {
		if frame.Bytes > 0 {
			f.remaining = frame.Bytes
		}
		f.remaining -= len(frame.Data)
		if f.remaining == 0 && f.reject {
			f.frames <- terminalstream.Frame{Type: "paste_error", Message: "image rejected"}
			return nil
		}
	}
	f.frames <- terminalstream.Frame{Type: "input_credit", Bytes: len(frame.Data)}
	if f.remaining == 0 {
		f.frames <- terminalstream.Frame{Type: "echo_ack", Seq: frame.Seq}
	}
	return nil
}
func TestNativeImageDeliveryOrdersPasteBeforeTextAndReturn(t *testing.T) {
	for _, reject := range []bool{false, true} {
		f := &nativeImageFixture{frames: make(chan terminalstream.Frame, 128), reject: reject}
		f.frames <- terminalstream.Frame{Type: "receipt", Capabilities: []string{terminalstream.CapabilityEchoAck}}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := sendNativeImages(ctx, f, [][]byte{[]byte(strings.Repeat("i", terminalstream.Window*2+1))}, "Describe this")
		cancel()
		f.mu.Lock()
		var inputs strings.Builder
		for _, frame := range f.sent {
			if frame.Type == "input" {
				inputs.Write(frame.Data)
			}
		}
		f.mu.Unlock()
		if reject {
			if err == nil || inputs.Len() != 0 {
				t.Fatalf("failed paste submitted: %v %q", err, inputs.String())
			}
		} else if err != nil || inputs.String() != "\x1b[200~Describe this\x1b[201~\r" {
			t.Fatalf("delivery: %v %q", err, inputs.String())
		}
	}
}
func TestNativeImageDeliveryCancellation(t *testing.T) {
	f := &nativeImageFixture{frames: make(chan terminalstream.Frame)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sendNativeImages(ctx, f, nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
