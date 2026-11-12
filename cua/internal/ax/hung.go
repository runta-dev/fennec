package ax

import (
	"sync"
	"time"
)

// Hung-app detection.
const (
	// MessagingTimeout bounds every accessibility read of an app.
	MessagingTimeout = 500 * time.Millisecond
	// MissFraction of MessagingTimeout spent on a read that returned no
	// window list makes it a miss.
	MissFraction = 0.9
	// HungAfter is how long misses must last before an app is hung.
	HungAfter = 2 * time.Second
	// LaunchGrace keeps a just listed app from being written off.
	LaunchGrace = 10 * time.Second
	// HungSkip is how long a hung app is skipped.
	HungSkip = 10 * time.Second
)

// IsMiss reports whether a window-list read counts as a miss: it took at
// least 90% of the messaging timeout and still returned no window list.
func IsMiss(took time.Duration, gotWindows bool) bool {
	return !gotWindows && took >= time.Duration(float64(MessagingTimeout)*MissFraction)
}

// Hung tracks apps that stop answering accessibility. An app whose misses
// have lasted HungAfter, and that has been listed for LaunchGrace, is hung
// and skipped for HungSkip; at its first miss after the skip it is hung
// again at once. While an app is missed or skipped its last known windows
// are kept, for the caller to flag stale.
type Hung[W any] struct {
	mu   sync.Mutex
	apps map[int]*hungApp[W]
}

type hungApp[W any] struct {
	firstSeen time.Time
	missSince time.Time // zero when the last read answered
	skipUntil time.Time
	hung      bool
	last      []W
}

func (h *Hung[W]) app(pid int, now time.Time) *hungApp[W] {
	if h.apps == nil {
		h.apps = map[int]*hungApp[W]{}
	}
	a := h.apps[pid]
	if a == nil {
		a = &hungApp[W]{firstSeen: now}
		h.apps[pid] = a
	}
	return a
}

// Skipping notes that pid is listed at now and reports whether it is being
// skipped as hung; if so it answers the app's last known windows.
func (h *Hung[W]) Skipping(pid int, now time.Time) ([]W, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.app(pid, now)
	if now.Before(a.skipUntil) {
		return append([]W(nil), a.last...), true
	}
	return nil, false
}

// Miss records a read of pid that did not answer in time and answers the
// app's last known windows.
func (h *Hung[W]) Miss(pid int, now time.Time) []W {
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.app(pid, now)
	if a.missSince.IsZero() {
		a.missSince = now
	}
	if a.hung || (now.Sub(a.missSince) >= HungAfter && now.Sub(a.firstSeen) >= LaunchGrace) {
		a.hung = true
		a.skipUntil = now.Add(HungSkip)
	}
	return append([]W(nil), a.last...)
}

// Answer records a read of pid that answered with ws.
func (h *Hung[W]) Answer(pid int, now time.Time, ws []W) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.app(pid, now)
	a.missSince = time.Time{}
	a.hung = false
	a.last = ws
}

// IsHung reports whether pid is marked hung.
func (h *Hung[W]) IsHung(pid int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.apps[pid]
	return a != nil && a.hung
}

// Last answers pid's last known windows.
func (h *Hung[W]) Last(pid int) []W {
	h.mu.Lock()
	defer h.mu.Unlock()
	if a := h.apps[pid]; a != nil {
		return append([]W(nil), a.last...)
	}
	return nil
}

// Forget drops apps not in keep, such as quit ones.
func (h *Hung[W]) Forget(keep map[int]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for pid := range h.apps {
		if !keep[pid] {
			delete(h.apps, pid)
		}
	}
}
