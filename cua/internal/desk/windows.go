//go:build darwin

// Package desk is the macOS desktop backend: it implements view.Source over
// the accessibility API and the window server.
package desk

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"fennec/cua/internal/ax"
	"fennec/cua/internal/view"
)

// driverApp is the bundled cua-driver's process name; its overlay and other
// windows are never listed, even when another copy of the driver runs.
const driverApp = "cua-driver"

// Hung-app detection.
const (
	MessagingTimeout = 500 * time.Millisecond
	missFraction     = 0.9
	hungAfter        = 2 * time.Second
	launchGrace      = 10 * time.Second
	hungSkip         = 10 * time.Second
)

// Desk reads the macOS desktop.
type Desk struct {
	// ExcludePIDs hides processes whose windows agents must never see, such
	// as fennec-cua itself and its driver.
	ExcludePIDs func() []int

	mu   sync.Mutex
	apps map[int]*appState
	now  func() time.Time
}

type appState struct {
	firstSeen time.Time
	missSince time.Time // zero when the last read answered
	skipUntil time.Time
	hung      bool
	last      []view.RawWindow
}

// New returns a desk.
func New() *Desk { return &Desk{apps: map[int]*appState{}, now: time.Now} }

func (d *Desk) excluded() map[int]bool {
	m := map[int]bool{os.Getpid(): true}
	if d.ExcludePIDs != nil {
		for _, p := range d.ExcludePIDs() {
			m[p] = true
		}
	}
	return m
}

// toDesk converts AX points to desktop coordinates (main display pixels).
func toDesk(r ax.Rect, scale float64) *[4]int {
	if r.W <= 0 && r.H <= 0 {
		return nil
	}
	return &[4]int{int(math.Round(r.X * scale)), int(math.Round(r.Y * scale)), int(math.Round(r.W * scale)), int(math.Round(r.H * scale))}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func boolean(v any) bool {
	b, _ := v.(bool)
	return b
}

func frame(pos, size any) (ax.Rect, bool) {
	p, ok1 := pos.(ax.Point)
	s, ok2 := size.(ax.Size)
	if !ok1 || !ok2 {
		return ax.Rect{}, false
	}
	return ax.Rect{X: p.X, Y: p.Y, W: s.W, H: s.H}, true
}

var windowAttrs = []string{"AXRole", "AXSubrole", "AXTitle", "AXPosition", "AXSize", "AXMinimized", "AXMain", "AXFocused", "AXChildren", "AXModal"}

// Windows lists top-level windows and sheets, front to back.
func (d *Desk) Windows(ctx context.Context) ([]view.RawWindow, error) {
	server, err := ax.ServerWindows(true)
	if err != nil {
		return nil, err
	}
	scale := ax.BackingScale()
	skip := d.excluded()
	// Z-order rank and app order from the window server.
	rank := map[uint32]int{}
	var pids []int
	seenPID := map[int]bool{}
	names := map[int]string{}
	shown := map[int]map[uint32]ax.Rect{}
	for i, w := range server {
		if w.Layer != 0 || w.Alpha <= 0 || skip[w.PID] || w.Owner == driverApp {
			continue
		}
		rank[w.ID] = i
		if shown[w.PID] == nil {
			shown[w.PID] = map[uint32]ax.Rect{}
		}
		shown[w.PID][w.ID] = w.Bounds
		if !seenPID[w.PID] {
			seenPID[w.PID] = true
			pids = append(pids, w.PID)
			names[w.PID] = w.Owner
		}
	}
	frontPID := focusedPID()
	// Apps answer independently; a slow one must not hold up the rest.
	per := make([][]view.RawWindow, len(pids))
	var wg sync.WaitGroup
	for i, pid := range pids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			per[i] = d.appWindows(pid, names[pid], pid == frontPID, scale, shown[pid])
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var out []view.RawWindow
	for _, ws := range per {
		out = append(out, ws...)
	}
	sort.SliceStable(out, func(i, j int) bool { return order(out[i], rank) < order(out[j], rank) })
	return out, nil
}

// order sorts by window server rank; sheets follow their owner.
func order(w view.RawWindow, rank map[uint32]int) float64 {
	key := w.Key
	bump := 0.0
	if w.OwnerKey != "" {
		key, bump = w.OwnerKey, 0.5
	}
	if id, ok := serverID(key); ok {
		if r, ok := rank[id]; ok {
			return float64(r) + bump
		}
	}
	return 1e9 + bump
}

func serverID(key string) (uint32, bool) {
	if len(key) < 4 || key[:3] != "cg:" {
		return 0, false
	}
	n, err := strconv.ParseUint(key[3:], 10, 32)
	return uint32(n), err == nil
}

// appWindows reads one app's windows, with hung-app bookkeeping.
func (d *Desk) appWindows(pid int, name string, frontmost bool, scale float64, shown map[uint32]ax.Rect) []view.RawWindow {
	now := d.now()
	d.mu.Lock()
	st := d.apps[pid]
	if st == nil {
		st = &appState{firstSeen: now}
		d.apps[pid] = st
	}
	if now.Before(st.skipUntil) {
		last := staleCopy(st.last)
		d.mu.Unlock()
		return last
	}
	d.mu.Unlock()

	app := ax.App(pid)
	defer app.Release()
	app.SetTimeout(MessagingTimeout.Seconds())
	start := d.now()
	attrs, err := app.Attrs([]string{"AXWindows", "AXFocusedWindow", "AXTitle"})
	took := d.now().Sub(start)
	var list []any
	if err == nil {
		list, _ = attrs[0].([]any)
	}
	if len(list) == 0 && took >= time.Duration(float64(MessagingTimeout)*missFraction) {
		return d.miss(pid, st, now)
	}
	d.mu.Lock()
	st.missSince = time.Time{}
	st.hung = false
	d.mu.Unlock()
	if title := str(attrs[2]); title != "" && err == nil {
		name = title
	}
	var focused *ax.Element
	if err == nil {
		focused, _ = attrs[1].(*ax.Element)
	}
	var out []view.RawWindow
	for _, item := range list {
		el, ok := item.(*ax.Element)
		if !ok {
			continue
		}
		v, err := el.Attrs(windowAttrs)
		if err != nil {
			continue
		}
		w := view.RawWindow{Handle: el, App: name, PID: pid, Title: str(v[2])}
		r, hasFrame := frame(v[3], v[4])
		minimized := boolean(v[5])
		if id, ok := el.WindowID(); ok && onScreen(shown, id, r, hasFrame) {
			w.Key = "cg:" + strconv.FormatUint(uint64(id), 10)
		} else if minimized {
			w.Key = fmt.Sprintf("ax:%d:%x", pid, el.Hash())
		} else {
			// Not on this Space's screen and not minimized: the desktop,
			// status items and other surfaces that are not windows.
			continue
		}
		w.Role = view.WindowRole(str(v[1]), false)
		if hasFrame {
			w.Bounds = toDesk(r, scale)
		}
		if minimized {
			w.Hidden = "minimized"
		}
		w.Active = frontmost && focused != nil && focused.Equal(el)
		out = append(out, w)
		// Sheets hang on the window as children.
		kids, _ := v[8].([]any)
		for _, k := range kids {
			sheet, ok := k.(*ax.Element)
			if !ok {
				continue
			}
			sv, err := sheet.Attrs([]string{"AXRole", "AXTitle", "AXPosition", "AXSize"})
			if err != nil || str(sv[0]) != "AXSheet" {
				continue
			}
			s := view.RawWindow{
				Handle:   sheet,
				Key:      fmt.Sprintf("ax:%d:%x", pid, sheet.Hash()),
				App:      name,
				PID:      pid,
				Title:    str(sv[1]),
				Role:     view.WindowRole("", true),
				OwnerKey: w.Key,
				Active:   w.Active,
			}
			if r, ok := frame(sv[2], sv[3]); ok {
				s.Bounds = toDesk(r, scale)
			}
			out = append(out, s)
		}
	}
	d.mu.Lock()
	st.last = out
	d.mu.Unlock()
	return out
}

// miss records an app that did not answer in time and returns its last
// known windows, flagged stale.
func (d *Desk) miss(pid int, st *appState, now time.Time) []view.RawWindow {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st.missSince.IsZero() {
		st.missSince = now
	}
	if st.hung || (now.Sub(st.missSince) >= hungAfter && now.Sub(st.firstSeen) >= launchGrace) {
		st.hung = true
		st.skipUntil = now.Add(hungSkip)
	}
	return staleCopy(st.last)
}

func staleCopy(ws []view.RawWindow) []view.RawWindow {
	out := make([]view.RawWindow, len(ws))
	for i, w := range ws {
		w.Stale = true
		w.Active = false
		out[i] = w
	}
	return out
}

// focusedPID is the frontmost app's pid, from the system-wide element.
func focusedPID() int {
	sys := ax.SystemWide()
	defer sys.Release()
	sys.SetTimeout(MessagingTimeout.Seconds())
	v, err := sys.Attr("AXFocusedApplication")
	if err != nil {
		return 0
	}
	app, ok := v.(*ax.Element)
	if !ok {
		return 0
	}
	defer app.Release()
	pid, _ := app.PID()
	return pid
}

// onScreen reports whether an AX window is one of its process's on-screen
// window-server windows: its id is listed and its frame matches within 1
// point on every edge. A window whose frame could not be read counts as
// shown when its id is listed.
func onScreen(shown map[uint32]ax.Rect, id uint32, r ax.Rect, hasFrame bool) bool {
	b, ok := shown[id]
	if !ok {
		return false
	}
	if !hasFrame {
		return true
	}
	near := func(a, b float64) bool { return a-b <= 1 && b-a <= 1 }
	return near(r.X, b.X) && near(r.Y, b.Y) && near(r.W, b.W) && near(r.H, b.H)
}
