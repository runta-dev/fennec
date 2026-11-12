package terminalstream

import (
	"context"
	"fmt"
	"io"
	"sync"
)

type mailbox struct {
	mu             sync.Mutex
	changed        chan struct{}
	frames         []Frame
	bytes, credits int
	resultBytes    int
	closed         bool
	err            error
}

func newMailbox() *mailbox { return &mailbox{changed: make(chan struct{})} }

func (box *mailbox) push(frame Frame) error {
	box.mu.Lock()
	defer box.mu.Unlock()
	if box.closed {
		return io.EOF
	}
	bytes, credits := box.bytes, box.credits
	if box.resultBytes+len(frame.Result) > 4<<20 {
		return fmt.Errorf("operation result budget exceeded")
	}
	if frame.Type == "data" {
		bytes += len(frame.Data)
	}
	if frame.Type == "input_credit" {
		credits += frame.Bytes
	}
	if bytes > Window || credits > Window || len(box.frames) >= 2*Window+8 {
		return fmt.Errorf("terminal flow window exceeded")
	}
	box.bytes, box.credits = bytes, credits
	box.resultBytes += len(frame.Result)
	box.frames = append(box.frames, frame)
	close(box.changed)
	box.changed = make(chan struct{})
	return nil
}

func (box *mailbox) next(ctx context.Context) (Frame, error) {
	for {
		box.mu.Lock()
		if len(box.frames) > 0 {
			frame := box.frames[0]
			box.resultBytes -= len(frame.Result)
			box.frames[0] = Frame{}
			box.frames = box.frames[1:]
			if len(box.frames) == 0 {
				box.frames = nil
			}
			if frame.Type == "data" {
				box.bytes -= len(frame.Data)
			}
			if frame.Type == "input_credit" {
				box.credits -= frame.Bytes
			}
			box.mu.Unlock()
			return frame, nil
		}
		if box.closed {
			err := box.err
			if err == nil {
				err = io.EOF
			}
			box.mu.Unlock()
			return Frame{}, err
		}
		changed := box.changed
		box.mu.Unlock()
		select {
		case <-ctx.Done():
			return Frame{}, ctx.Err()
		case <-changed:
		}
	}
}

func (box *mailbox) close(err error) {
	box.mu.Lock()
	defer box.mu.Unlock()
	if !box.closed {
		box.closed = true
		box.err = err
		close(box.changed)
	}
}
