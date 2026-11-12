package record

import (
	"context"
	"errors"
	"sync"

	"fennec/cua/internal/view"
)

// LiveDesk adapts the daemon's desktop backend to the recorder.
type LiveDesk struct {
	Source view.Source
	// Capture shoots one window alone as a PNG (cua-driver's window
	// capture in production).
	Capture func(ctx context.Context, w view.RawWindow) ([]byte, error)
	// Web turns an app's web page tree on and answers whether it was on
	// before; Unweb turns it off again. Nil leaves the tree as it is.
	Web   func(ctx context.Context, pid int) (wasOn bool, err error)
	Unweb func(ctx context.Context, pid int) error
	// BundleOf names a process's app bundle without .app; nil uses
	// BundleStem.
	BundleOf func(pid int) string

	mu   sync.Mutex
	lit  []int // processes whose tree this recording turned on
	seen map[int]bool
}

// Windows lists windows front to back.
func (d *LiveDesk) Windows(ctx context.Context) ([]view.RawWindow, error) {
	return d.Source.Windows(ctx)
}

// At hit-tests a point through the backend.
func (d *LiveDesk) At(ctx context.Context, x, y int) (*Hit, error) {
	n, path, win, _, err := d.Source.At(ctx, x, y)
	if err != nil || n == nil {
		return nil, err
	}
	return HitOf(*n, path, win), nil
}

// HitOf turns a backend hit test into a recorded hit.
func HitOf(n view.RawNode, path []view.RawNode, win *view.RawWindow) *Hit {
	h := &Hit{Role: n.Role, Name: n.Name, Desc: n.Desc, Secret: n.HasState("protected")}
	for _, p := range path {
		h.Path = append(h.Path, PathNode{Role: p.Role, Name: p.Name})
	}
	if win != nil {
		h.PID = win.PID
		w := PathNode{Role: win.Role, Name: win.Title, Key: win.Key}
		if w.Role == "" {
			w.Role = "window"
		}
		h.Window = &w
	}
	if n.Page != "" {
		h.Web = &Web{URL: n.Page, ID: n.DOMID}
	}
	return h
}

// Shot captures a window.
func (d *LiveDesk) Shot(ctx context.Context, w view.RawWindow) ([]byte, error) {
	if d.Capture == nil {
		return nil, errors.New("no window capture")
	}
	return d.Capture(ctx, w)
}

// HoldWeb turns a process's page tree on, remembering whether this
// recording turned it on.
func (d *LiveDesk) HoldWeb(ctx context.Context, pid int) error {
	if d.Web == nil {
		return nil
	}
	d.mu.Lock()
	if d.seen == nil {
		d.seen = map[int]bool{}
	}
	if d.seen[pid] {
		d.mu.Unlock()
		return nil
	}
	d.seen[pid] = true
	d.mu.Unlock()
	was, err := d.Web(ctx, pid)
	if err != nil {
		return err
	}
	if !was {
		d.mu.Lock()
		d.lit = append(d.lit, pid)
		d.mu.Unlock()
	}
	return nil
}

// ReleaseWeb turns off the trees this recording turned on.
func (d *LiveDesk) ReleaseWeb(ctx context.Context) {
	d.mu.Lock()
	lit := d.lit
	d.lit, d.seen = nil, nil
	d.mu.Unlock()
	if d.Unweb == nil {
		return
	}
	for _, pid := range lit {
		_ = d.Unweb(ctx, pid)
	}
}

// Bundle names a process's bundle.
func (d *LiveDesk) Bundle(pid int) string {
	if d.BundleOf == nil {
		return BundleStem(pid)
	}
	return d.BundleOf(pid)
}
