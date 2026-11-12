package actions

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"fennec/cua/internal/view"
)

// ListFresh is how old a window list may be and still stand in for a new
// read.
const ListFresh = 25 * time.Millisecond

// Lists keeps the last window list read through a watched backend, so the
// plan executor and the actions executor share one read: a listing answers
// the kept list while it is at most ListFresh old and no other desktop call
// (a tree or element read, any input) came in between. Any other call drops
// it, and so does Drop.
type Lists struct {
	// Now is the clock; time.Now by default.
	Now func() time.Time

	mu   sync.Mutex
	list []view.RawWindow
	at   time.Time
	kept bool
	// Reads counts listings the backend answered; Reused those the kept
	// list answered.
	Reads, Reused int
}

func (l *Lists) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Drop forgets the kept list: the next listing reads the desktop.
func (l *Lists) Drop() {
	l.mu.Lock()
	l.kept = false
	l.list = nil
	l.mu.Unlock()
}

// Fresh answers the kept list while it stands.
func (l *Lists) Fresh() ([]view.RawWindow, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.kept || l.now().Sub(l.at) > ListFresh {
		return nil, false
	}
	return append([]view.RawWindow(nil), l.list...), true
}

func (l *Lists) keep(list []view.RawWindow, at time.Time) {
	l.mu.Lock()
	l.list = append([]view.RawWindow(nil), list...)
	l.at = at
	l.kept = true
	l.Reads++
	l.mu.Unlock()
}

// Watch wraps a backend so that its window listings share l: a listing
// answers l's kept list while it stands and keeps every list it reads;
// every other desktop call drops it. Pure queries (Same, Scale, Chromium,
// LockedDelivery, ScreenLocked) touch nothing and keep it.
func Watch(b Backend, l *Lists) Backend {
	return &watched{Backend: b, l: l}
}

// ListsOf is the shared list of a watched backend, nil for another.
func ListsOf(b Backend) *Lists {
	if w, ok := b.(*watched); ok {
		return w.l
	}
	return nil
}

type watched struct {
	Backend
	l *Lists
}

func (w *watched) Windows(ctx context.Context) ([]view.RawWindow, error) {
	if list, ok := w.l.Fresh(); ok {
		w.l.mu.Lock()
		w.l.Reused++
		w.l.mu.Unlock()
		return list, nil
	}
	// The list is as old as the moment the read began.
	at := w.l.now()
	list, err := w.Backend.Windows(ctx)
	if err != nil {
		w.l.Drop()
		return nil, err
	}
	w.l.keep(list, at)
	return list, nil
}

func (w *watched) Tree(ctx context.Context, win view.RawWindow, opts view.TreeOptions) (view.Tree, error) {
	w.l.Drop()
	return w.Backend.Tree(ctx, win, opts)
}

func (w *watched) Read(ctx context.Context, h view.Handle, full, tail bool) (view.RawNode, bool, error) {
	w.l.Drop()
	return w.Backend.Read(ctx, h, full, tail)
}

func (w *watched) At(ctx context.Context, x, y int) (*view.RawNode, []view.RawNode, *view.RawWindow, *view.RawWindow, error) {
	w.l.Drop()
	return w.Backend.At(ctx, x, y)
}

func (w *watched) Focused(ctx context.Context, pid int) (*view.RawNode, error) {
	w.l.Drop()
	return w.Backend.Focused(ctx, pid)
}

func (w *watched) Perform(ctx context.Context, h view.Handle, action string) error {
	w.l.Drop()
	return w.Backend.Perform(ctx, h, action)
}

func (w *watched) SetValue(ctx context.Context, h view.Handle, value string) error {
	w.l.Drop()
	return w.Backend.SetValue(ctx, h, value)
}

func (w *watched) Select(ctx context.Context, h view.Handle) error {
	w.l.Drop()
	return w.Backend.Select(ctx, h)
}

func (w *watched) FocusElement(ctx context.Context, h view.Handle) error {
	w.l.Drop()
	return w.Backend.FocusElement(ctx, h)
}

func (w *watched) Ancestors(ctx context.Context, h view.Handle) ([]view.RawNode, error) {
	w.l.Drop()
	return w.Backend.Ancestors(ctx, h)
}

func (w *watched) Raise(ctx context.Context, win view.RawWindow) error {
	w.l.Drop()
	return w.Backend.Raise(ctx, win)
}

func (w *watched) CloseWindow(ctx context.Context, win view.RawWindow) error {
	w.l.Drop()
	return w.Backend.CloseWindow(ctx, win)
}

func (w *watched) Driver(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	w.l.Drop()
	return w.Backend.Driver(ctx, tool, args)
}

func (w *watched) HitClick(ctx context.Context, win view.RawWindow, n view.RawNode, count int) (json.RawMessage, error) {
	w.l.Drop()
	return w.Backend.HitClick(ctx, win, n, count)
}
