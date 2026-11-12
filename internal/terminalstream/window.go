package terminalstream

import "sync"

type CreditWindow struct{ window *flowWindow }

func NewCreditWindow() *CreditWindow              { return &CreditWindow{newWindow()} }
func (window *CreditWindow) Take(maximum int) int { return window.window.take(maximum) }
func (window *CreditWindow) Add(count int) bool   { return window.window.add(count) }
func (window *CreditWindow) Close()               { window.window.close() }

type flowWindow struct {
	mu        sync.Mutex
	changed   *sync.Cond
	available int
	capacity  int
	closed    bool
}

func newWindow() *flowWindow { return newWindowSize(Window) }

func newWindowSize(capacity int) *flowWindow {
	window := &flowWindow{available: capacity, capacity: capacity}
	window.changed = sync.NewCond(&window.mu)
	return window
}

func (window *flowWindow) take(maximum int) int {
	window.mu.Lock()
	defer window.mu.Unlock()
	for window.available == 0 && !window.closed {
		window.changed.Wait()
	}
	if window.closed {
		return 0
	}
	count := min(maximum, window.available)
	window.available -= count
	return count
}

func (window *flowWindow) add(count int) bool {
	window.mu.Lock()
	defer window.mu.Unlock()
	if count <= 0 || count > window.capacity-window.available || window.closed {
		return false
	}
	window.available += count
	window.changed.Broadcast()
	return true
}

func (window *flowWindow) close() {
	window.mu.Lock()
	window.closed = true
	window.changed.Broadcast()
	window.mu.Unlock()
}
