package driver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

// WindowStates remembers which windows the driver holds a snapshot of, so a
// window-pixel action reads a window's state once and reuses it while the
// window has not moved and the display scale is unchanged. At scale 1 window
// pixels are points and no read is needed.
type WindowStates struct {
	mu   sync.Mutex
	seen map[uint32]windowSeen
}

type windowSeen struct {
	bounds [4]float64
	scale  float64
}

// Need reports whether window id must be read before a pixel action.
func (w *WindowStates) Need(id uint32, bounds [4]float64, scale float64) bool {
	if scale == 1 {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.seen[id]
	return !ok || s.bounds != bounds || s.scale != scale
}

// Read records that window id was read at bounds and scale.
func (w *WindowStates) Read(id uint32, bounds [4]float64, scale float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil {
		w.seen = map[uint32]windowSeen{}
	}
	w.seen[id] = windowSeen{bounds: bounds, scale: scale}
}

// Forget drops window id, as when the driver forgot its snapshot.
func (w *WindowStates) Forget(id uint32) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.seen, id)
}

// Forgotten reports a driver refusal for a window it holds no snapshot of;
// nothing was sent, so reading the window and sending again is safe.
func Forgotten(err error) bool {
	var de *Error
	if !errors.As(err, &de) {
		return false
	}
	return de.Reason == ReasonNeverRead || de.Code == "screenshot_context_missing" || strings.Contains(de.Message, ReasonNeverRead)
}

// WindowCall sends a window-pixel action (tool with args) for window id of
// pid. It reads the window's state first when WindowStates says so, and when
// the driver answers that it forgot the window, it reads again and sends the
// action once more. A result the driver marks isError is an *Error.
func (c *Client) WindowCall(ctx context.Context, laneName string, pid int, id uint32, bounds [4]float64, scale float64, tool string, args map[string]any) (json.RawMessage, error) {
	read := func() error {
		raw, err := c.Call(ctx, laneName, "get_window_state", map[string]any{"pid": pid, "window_id": id, "include_screenshot": true})
		if err == nil {
			err = Check("get_window_state", raw)
		}
		if err != nil {
			return err
		}
		c.windows.Read(id, bounds, scale)
		return nil
	}
	send := func() (json.RawMessage, error) {
		raw, err := c.Call(ctx, laneName, tool, args)
		if err == nil {
			err = Check(tool, raw)
		}
		if err != nil {
			return nil, err
		}
		return raw, nil
	}
	if c.windows.Need(id, bounds, scale) {
		if err := read(); err != nil {
			return nil, err
		}
	}
	raw, err := send()
	if Forgotten(err) {
		c.windows.Forget(id)
		if err := read(); err != nil {
			return nil, err
		}
		return send()
	}
	return raw, err
}
