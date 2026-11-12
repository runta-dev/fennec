//go:build darwin

// Package desk is the macOS desktop backend: it implements view.Source over
// the accessibility API and the window server.
package desk

import (
	"context"
	"os"
	"sort"
	"sync"
	"time"

	"fennec/cua/internal/ax"
	"fennec/cua/internal/view"
)

// MessagingTimeout bounds every accessibility read of an app.
const MessagingTimeout = ax.MessagingTimeout

// Desk reads the macOS desktop.
type Desk struct {
	// ExcludePIDs hides processes whose windows agents must never see, such
	// as fennec-cua itself and its driver.
	ExcludePIDs func() []int
	// DriverPath is the bundled cua-driver's binary. When set, windows of
	// the process at that canonical path are never listed; when empty the
	// driver is recognised by its process name.
	DriverPath string
	// OtherSpaces lists windows on other Spaces (marked Space "other");
	// the view shows them only under background or auto input.
	OtherSpaces bool

	hung ax.Hung[view.RawWindow]
	mu   sync.Mutex
	// lastActive is each app's window key that was active at its last
	// answer, for choosing which stale window is active.
	lastActive map[int]string
	now        func() time.Time
}

// New returns a desk.
func New() *Desk { return &Desk{now: time.Now, OtherSpaces: true} }

func (d *Desk) clock() time.Time {
	if d.now == nil {
		return time.Now()
	}
	return d.now()
}

func (d *Desk) excluded() map[int]bool {
	m := map[int]bool{os.Getpid(): true}
	if d.ExcludePIDs != nil {
		for _, p := range d.ExcludePIDs() {
			m[p] = true
		}
	}
	return m
}

// skipper hides excluded processes and the driver's windows.
func (d *Desk) skipper() func(ax.ServerWindow) bool {
	skip := d.excluded()
	drv := ax.DriverFilter{Path: d.DriverPath}
	return func(w ax.ServerWindow) bool { return skip[w.PID] || drv.Is(w) }
}

// toDesk converts AX points to desktop coordinates (main display pixels).
func toDesk(r ax.Rect, scale float64) *[4]int { return ax.ToDesk(r, scale) }

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

// Windows lists top-level windows and sheets, front to back, then windows
// on other Spaces.
func (d *Desk) Windows(ctx context.Context) ([]view.RawWindow, error) {
	server, err := ax.ServerWindows(true)
	if err != nil {
		return nil, err
	}
	scale := ax.BackingScale()
	skip := d.skipper()
	l := ax.ListApps(server, skip)
	frontPID := ax.FrontmostPID()
	if frontPID == 0 {
		frontPID = focusedPID()
	}
	// Apps answer independently; a slow one must not hold up the rest.
	per := make([][]view.RawWindow, len(l.PIDs))
	var wg sync.WaitGroup
	for i, pid := range l.PIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			per[i] = d.appWindows(pid, l.Names[pid], pid == frontPID, scale, l.Shown[pid])
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
	sort.SliceStable(out, func(i, j int) bool { return order(out[i], l.Rank) < order(out[j], l.Rank) })
	d.markStaleActive(out, frontPID)
	if d.OtherSpaces {
		listed := map[uint32]bool{}
		for _, w := range out {
			if id, ok := ax.WindowNumber(w.Key); ok {
				listed[id] = true
			}
		}
		others, err := ax.OtherSpaceWindows(listed, skip)
		if err == nil {
			out = append(out, otherSpaceWindows(others, scale)...)
		}
	}
	return out, nil
}

// otherSpaceWindows lists windows the window server places on another
// Space; accessibility does not reach them there, so they carry no handle.
func otherSpaceWindows(ws []ax.ServerWindow, scale float64) []view.RawWindow {
	out := make([]view.RawWindow, 0, len(ws))
	for _, w := range ws {
		out = append(out, view.RawWindow{
			Key:    ax.WindowKey(w.ID),
			App:    w.Owner,
			PID:    w.PID,
			Title:  w.Title,
			Role:   view.WindowRole("", false),
			Bounds: ax.ToDesk(w.Bounds, scale),
			Space:  "other",
		})
	}
	return out
}

// markStaleActive reports one stale window of the frontmost app active when
// no answering window is.
func (d *Desk) markStaleActive(ws []view.RawWindow, frontPID int) {
	d.mu.Lock()
	cands := make([]ax.StaleCandidate, len(ws))
	for i, w := range ws {
		cands[i] = ax.StaleCandidate{PID: w.PID, Stale: w.Stale, Active: w.Active, WasActive: w.Stale && w.Key != "" && d.lastActive[w.PID] == w.Key}
	}
	d.mu.Unlock()
	if i := ax.StaleActive(cands, frontPID); i >= 0 {
		ws[i].Active = true
	}
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

func serverID(key string) (uint32, bool) { return ax.WindowNumber(key) }

// appWindows reads one app's windows, with hung-app bookkeeping.
func (d *Desk) appWindows(pid int, name string, frontmost bool, scale float64, shown map[uint32]ax.Rect) []view.RawWindow {
	now := d.clock()
	if last, skipping := d.hung.Skipping(pid, now); skipping {
		return staleCopy(last)
	}
	app := ax.App(pid)
	defer app.Release()
	app.SetTimeout(MessagingTimeout.Seconds())
	start := d.clock()
	attrs, err := app.Attrs([]string{"AXWindows", "AXFocusedWindow", "AXTitle"})
	took := d.clock().Sub(start)
	var list []any
	if err == nil {
		list, _ = attrs[0].([]any)
	}
	if ax.IsMiss(took, len(list) > 0) {
		return staleCopy(d.hung.Miss(pid, now))
	}
	if err == nil {
		if title := str(attrs[2]); title != "" {
			name = title
		}
	}
	var focused *ax.Element
	if err == nil {
		focused, _ = attrs[1].(*ax.Element)
	}
	var out []view.RawWindow
	active := ""
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
		id, hasID := el.WindowID()
		onScreen := false
		if hasID {
			if b, listed := shown[id]; listed {
				onScreen = ax.FrameShown([]ax.Rect{b}, r, hasFrame)
			}
		}
		switch ax.Unseen(onScreen, minimized, false) {
		case "":
			w.Key = ax.WindowKey(id)
		case ax.UnseenMinimized:
			w.Key = ax.ProcessScopedKey(pid, el.Hash())
			w.Hidden = "minimized"
		default:
			// Not on this Space's screen and not minimized: the desktop,
			// status items and other surfaces that are not windows.
			continue
		}
		w.Role = view.WindowRole(str(v[1]), false)
		if hasFrame {
			w.Bounds = toDesk(r, scale)
		}
		w.Active = frontmost && focused != nil && focused.Equal(el)
		if focused != nil && focused.Equal(el) {
			active = w.Key
		}
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
				Key:      ax.ProcessScopedKey(pid, sheet.Hash()),
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
	d.hung.Answer(pid, now, out)
	d.mu.Lock()
	if d.lastActive == nil {
		d.lastActive = map[int]string{}
	}
	if active != "" {
		d.lastActive[pid] = active
	}
	d.mu.Unlock()
	return out
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

// focusedPID is the frontmost app's pid from the system-wide element; a
// fallback when the process manager does not answer.
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
