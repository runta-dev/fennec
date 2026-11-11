package terminalstream

import (
	"sync"
	"time"
)

// Keep the receipt and initial paint fragments in one output batch. Tiny
// consecutive SSH writes otherwise incurred another acknowledgement delay.
// The receipt is never held beyond 25ms, even for a silent program.
func coalesceReceipt(write func(...Frame) error, receipt Frame) (func(Frame) error, func()) {
	var mu sync.Mutex
	pending := []Frame{receipt}
	flushed := false
	generation := uint64(0)
	var quiet *time.Timer
	flushLocked := func() error {
		if flushed {
			return nil
		}
		flushed = true
		frames := pending
		pending = nil
		return write(frames...)
	}
	deadline := time.AfterFunc(25*time.Millisecond, func() { mu.Lock(); defer mu.Unlock(); _ = flushLocked() })
	emit := func(frame Frame) error {
		mu.Lock()
		defer mu.Unlock()
		if flushed {
			return write(frame)
		}
		pending = append(pending, frame)
		if frame.Type == "exit" || frame.Type == "error" {
			return flushLocked()
		}
		generation++
		current := generation
		if quiet != nil {
			quiet.Stop()
		}
		quiet = time.AfterFunc(8*time.Millisecond, func() {
			mu.Lock()
			defer mu.Unlock()
			if generation == current {
				_ = flushLocked()
			}
		})
		return nil
	}
	finish := func() {
		mu.Lock()
		defer mu.Unlock()
		deadline.Stop()
		if quiet != nil {
			quiet.Stop()
		}
		_ = flushLocked()
	}
	return emit, finish
}
