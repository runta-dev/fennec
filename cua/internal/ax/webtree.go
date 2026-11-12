package ax

import (
	"errors"
	"fmt"
	"sync"
)

// ErrWebTreeRefused is a hold on an app outside the allowlist.
var ErrWebTreeRefused = errors.New("the app is outside the allowlist; its web tree is left alone")

// WebTree turns a Chromium or Electron app's web accessibility tree on for
// holders (an MCP session, a run, a recording). The tree stays on while any
// holder remains and goes off when the last one lets go, but only when it
// was off before the first hold. Concurrent first holds of one app are
// serialised so its original state is recorded once.
type WebTree struct {
	// Get reads whether pid's web tree is on.
	Get func(pid int) (bool, error)
	// Set turns pid's web tree on or off.
	Set func(pid int, on bool) error
	// Allowed, when set, says whether pid may be touched at all.
	Allowed func(pid int) bool

	mu   sync.Mutex
	apps map[int]*webApp
}

type webApp struct {
	mu      sync.Mutex // serialises first holds and the final release
	holders map[string]bool
	wasOn   bool
	known   bool
}

func (t *WebTree) app(pid int) *webApp {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.apps == nil {
		t.apps = map[int]*webApp{}
	}
	a := t.apps[pid]
	if a == nil {
		a = &webApp{holders: map[string]bool{}}
		t.apps[pid] = a
	}
	return a
}

// Hold turns pid's web tree on for holder and reports whether it was
// already on before the first hold. Holding twice is one hold.
func (t *WebTree) Hold(pid int, holder string) (wasOn bool, err error) {
	if t.Allowed != nil && !t.Allowed(pid) {
		return false, ErrWebTreeRefused
	}
	a := t.app(pid)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.holders) == 0 {
		on, err := t.Get(pid)
		if err != nil {
			return false, fmt.Errorf("read the web tree switch: %w", err)
		}
		if !on {
			if err := t.Set(pid, true); err != nil {
				return false, fmt.Errorf("turn the web tree on: %w", err)
			}
		}
		a.wasOn, a.known = on, true
	}
	a.holders[holder] = true
	return a.wasOn, nil
}

// Release lets go of holder's hold on pid; the last release turns the tree
// off when it was off originally.
func (t *WebTree) Release(pid int, holder string) error {
	t.mu.Lock()
	a := t.apps[pid]
	t.mu.Unlock()
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.holders[holder] {
		return nil
	}
	delete(a.holders, holder)
	if len(a.holders) > 0 || !a.known {
		return nil
	}
	a.known = false
	if a.wasOn {
		return nil
	}
	return t.Set(pid, false)
}

// ReleaseHolder lets go of every hold of holder, as when a session, run or
// recording ends however it ends.
func (t *WebTree) ReleaseHolder(holder string) error {
	t.mu.Lock()
	var pids []int
	for pid, a := range t.apps {
		a.mu.Lock()
		if a.holders[holder] {
			pids = append(pids, pid)
		}
		a.mu.Unlock()
	}
	t.mu.Unlock()
	var errs []error
	for _, pid := range pids {
		errs = append(errs, t.Release(pid, holder))
	}
	return errors.Join(errs...)
}

// Holders counts pid's holders.
func (t *WebTree) Holders(pid int) int {
	t.mu.Lock()
	a := t.apps[pid]
	t.mu.Unlock()
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.holders)
}
