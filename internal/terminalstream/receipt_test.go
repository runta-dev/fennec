package terminalstream

import (
	"testing"
	"time"
)

func TestFirstOutputSharesReceiptWrite(t *testing.T) {
	batches := make(chan []Frame, 3)
	emit, finish := coalesceReceipt(func(frames ...Frame) error { batches <- frames; return nil }, Frame{Type: "receipt"})
	defer finish()
	if err := emit(Frame{Type: "data", Data: []byte("ready")}); err != nil {
		t.Fatal(err)
	}
	batch := <-batches
	if len(batch) != 2 || batch[0].Type != "receipt" || batch[1].Type != "data" {
		t.Fatal(batch)
	}
	if err := emit(Frame{Type: "exit"}); err != nil {
		t.Fatal(err)
	}
	batch = <-batches
	if len(batch) != 1 || batch[0].Type != "exit" {
		t.Fatal(batch)
	}
}
func TestSilentTerminalReceiptDoesNotWaitForOutput(t *testing.T) {
	received := make(chan []Frame, 1)
	_, finish := coalesceReceipt(func(frames ...Frame) error { received <- frames; return nil }, Frame{Type: "receipt"})
	defer finish()
	select {
	case frames := <-received:
		if len(frames) != 1 || frames[0].Type != "receipt" {
			t.Fatal(frames)
		}
	case <-time.After(time.Second):
		t.Fatal("receipt withheld for silent program")
	}
}
