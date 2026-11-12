package record

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/draw"
	"image/png"
	"math"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/view"
)

// queue is an unbounded input queue: the tap never blocks on it.
type queue struct {
	mu     sync.Mutex
	items  []Input
	closed bool
	signal chan struct{}
}

func newQueue() *queue { return &queue{signal: make(chan struct{}, 1)} }

func (q *queue) push(in Input) bool {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	q.items = append(q.items, in)
	q.mu.Unlock()
	q.wake()
	return true
}

func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.wake()
}

func (q *queue) wake() {
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// pop takes the next input; ok is false once the queue is closed and empty.
// more is false when nothing is queued right now.
func (q *queue) pop() (in Input, more, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) > 0 {
		in = q.items[0]
		q.items = q.items[1:]
		return in, true, true
	}
	return Input{}, false, !q.closed
}

// session is one recording as the worker runs it.
type session struct {
	id       string
	apps     []string
	perPoint float64
	settle   time.Duration
	self     int
	desk     Desk
	inputs   *queue
	started  time.Time
	now      func() time.Time
}

// frame is a capture of one window, from which click crops are cut.
type frame struct {
	key    string
	png    []byte
	factor float64 // pixels per desktop unit
	bounds [4]float64
	at     time.Time
}

// work runs a session until its inputs end and answers the recording.
func (s *session) work(ctx context.Context) *Recording {
	if s.now == nil {
		s.now = time.Now
	}
	if s.settle <= 0 {
		s.settle = Settle
	}
	if s.perPoint <= 0 {
		s.perPoint = 1
	}
	t0 := s.started
	if t0.IsZero() {
		t0 = s.now()
	}
	since := func() int64 { return s.now().Sub(t0).Milliseconds() }
	asked := map[int]bool{}
	windows := s.list(ctx)
	s.light(ctx, windows, asked)
	front := frontOf(windows)
	fr := s.framed(ctx, windows, front)
	acted := t0
	var settle *time.Timer
	var settleC <-chan time.Time
	var events []Event
	hotkey := -1

	push := func(e Event) {
		events = append(events, e)
		acted = s.now()
		if settle != nil {
			settle.Stop()
		}
		settle = time.NewTimer(s.settle)
		settleC = settle.C
	}

	for {
		in, more, ok := s.inputs.pop()
		if !ok {
			break
		}
		if !more {
			select {
			case <-s.inputs.signal:
			case <-settleC:
				settleC = nil
				windows = s.list(ctx)
				s.light(ctx, windows, asked)
				now := frontOf(windows)
				if e := s.came(now, front, since()); e != nil {
					events = append(events, *e)
				}
				fr = s.framed(ctx, windows, now)
				front = now
			}
			continue
		}
		t := since()
		switch in.Kind {
		case InputNote:
			events = append(events, Event{T: t, Kind: KindNote, Text: in.Note})
		case InputHotkey:
			hotkey = in.Code
		case InputKey:
			if in.Code == hotkey {
				continue
			}
			pid := in.PID
			if pid > 0 && !hasPID(windows, pid) {
				windows = s.list(ctx)
			}
			var app string
			if pid > 0 {
				app = appOf(windows, pid)
			} else if front != nil {
				app = front.App
			}
			switch {
			case (app == "" && pid > 0) || (pid > 0 && pid == s.self) || !keeps(s.desk, app, pid, s.apps):
				push(Event{T: t, Kind: KindSkipped})
			case in.Secret:
				push(Event{T: t, Kind: KindKey, Secret: true, App: app})
			default:
				code := in.Code
				e := Event{T: t, Kind: KindKey, Code: &code, Text: in.Text, Mods: in.Mods, App: app}
				if in.Read {
					e.FocusRead, e.Focus = true, in.Focus
				}
				push(e)
			}
		case InputPassword:
			windows = s.list(ctx)
			app := appOf(windows, in.PID)
			if app == "" || !keeps(s.desk, app, in.PID, s.apps) {
				push(Event{T: t, Kind: KindSkipped})
				continue
			}
			e := Event{T: t, Kind: KindPassword, App: app, FocusRead: true}
			if in.Focus != nil {
				f := *in.Focus
				f.Secure = true
				e.Focus = &f
			}
			push(e)
		case InputClick:
			hit := in.Hit
			if hit == nil && s.desk != nil {
				hit, _ = s.desk.At(ctx, int(math.Round(in.X)), int(math.Round(in.Y)))
			}
			pid := 0
			if hit != nil {
				pid = hit.PID
			}
			if pid > 0 && pid == s.self {
				continue
			}
			windows = s.list(ctx)
			s.light(ctx, windows, asked)
			win := windowOf(windows, hit)
			app := ""
			if win != nil {
				app = win.App
			}
			if app == "" {
				app = appOf(windows, pid)
			}
			if (app == "" && pid > 0) || !keeps(s.desk, app, pid, s.apps) {
				push(Event{T: t, Kind: KindSkipped})
				continue
			}
			late := fr == nil || fr.at.Before(acted) || win == nil || win.Key != fr.key
			if late {
				fr = s.framed(ctx, windows, win)
			}
			x, y := in.X, in.Y
			e := Event{T: t, Kind: KindClick, X: &x, Y: &y, Button: "left", Count: in.Count, Mods: in.Mods, App: app, Root: rooted(hit), Late: late}
			if in.Right {
				e.Button = "right"
			}
			if e.Count < 1 {
				e.Count = 1
			}
			if hit != nil {
				e.Element = &Element{Role: hit.Role, Name: hit.Name, Desc: hit.Desc}
				e.Path = hit.Path
				e.Secret = hit.Secret
				if hit.Web != nil && hit.Web.URL != "" {
					w := *hit.Web
					e.Web = &w
				}
			}
			if win != nil {
				e.Window = &EventWindow{Title: win.Title, Bounds: win.Bounds}
				if fr != nil && fr.key == win.Key {
					if b64, scale, off, ok := cropAround(fr, x, y, s.perPoint); ok {
						e.Crop, e.CropScale, e.CropOffset = b64, scale, &off
					}
				}
			}
			push(e)
		}
	}
	if settle != nil {
		settle.Stop()
	}
	if hotkey >= 0 && len(events) > 0 {
		last := events[len(events)-1]
		if last.Kind == KindKey && last.Code != nil && *last.Code == hotkey {
			events = events[:len(events)-1]
		}
	}
	if e := s.came(frontOf(s.list(ctx)), front, since()); e != nil {
		events = append(events, *e)
	}
	if s.desk != nil {
		s.desk.ReleaseWeb(ctx)
	}
	if events == nil {
		events = []Event{}
	}
	apps := s.apps
	if apps == nil {
		apps = []string{}
	}
	return &Recording{ID: s.id, StartedMs: t0.UnixMilli(), StoppedMs: s.now().UnixMilli(), Apps: apps, Events: events}
}

func (s *session) list(ctx context.Context) []view.RawWindow {
	if s.desk == nil {
		return nil
	}
	ws, err := s.desk.Windows(ctx)
	if err != nil {
		return nil
	}
	return ws
}

// light holds the web page tree on in each kept app, once per process: a
// Chromium browser builds it only for an assistive app that asks.
func (s *session) light(ctx context.Context, ws []view.RawWindow, asked map[int]bool) {
	for _, w := range ws {
		if w.PID <= 0 || asked[w.PID] || !keeps(s.desk, w.App, w.PID, s.apps) {
			continue
		}
		asked[w.PID] = true
		_ = s.desk.HoldWeb(ctx, w.PID)
	}
}

// came answers an after event when a different kept window is now in front.
func (s *session) came(now, was *view.RawWindow, t int64) *Event {
	if now == nil || (was != nil && was.Title == now.Title) || !keeps(s.desk, now.App, now.PID, s.apps) {
		return nil
	}
	return &Event{T: t, Kind: KindAfter, Window: &EventWindow{Title: now.Title}, Role: now.Role, App: now.App}
}

// framed captures the window a crop will be cut from: the window a sheet
// hangs on, when w is a sheet, and only a window the recording keeps.
func (s *session) framed(ctx context.Context, ws []view.RawWindow, w *view.RawWindow) *frame {
	if w == nil || s.desk == nil {
		return nil
	}
	cur := *w
	for i := 0; i < len(ws) && cur.OwnerKey != ""; i++ {
		found := false
		for _, o := range ws {
			if o.Key == cur.OwnerKey {
				cur, found = o, true
				break
			}
		}
		if !found {
			return nil
		}
	}
	if cur.Bounds == nil || !keeps(s.desk, cur.App, cur.PID, s.apps) {
		return nil
	}
	data, err := s.desk.Shot(ctx, cur)
	if err != nil || len(data) == 0 {
		return nil
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	b := cur.Bounds
	bounds := [4]float64{float64(b[0]), float64(b[1]), float64(b[2]), float64(b[3])}
	factor := float64(cfg.Width) / math.Max(bounds[2], 1)
	// A capture whose height does not fit the window shows something else,
	// such as a sheet in front of it.
	if math.Abs(float64(cfg.Height)/factor-bounds[3]) > 2 {
		return nil
	}
	return &frame{key: cur.Key, png: data, factor: factor, bounds: bounds, at: s.now()}
}

// cropAround cuts a crop of cropW by cropH points around a desktop point,
// clipped at the window's edges, and answers its scale and the point's
// offset from its centre in desktop units, rounded to 0.1.
func cropAround(f *frame, x, y, perPoint float64) (string, float64, [2]float64, bool) {
	w, h := cropW*perPoint, cropH*perPoint
	rx, ry := x-f.bounds[0], y-f.bounds[1]
	left, top := math.Max(rx-w/2, 0), math.Max(ry-h/2, 0)
	right, bottom := math.Min(rx+w/2, f.bounds[2]), math.Min(ry+h/2, f.bounds[3])
	if right <= left || bottom <= top {
		return "", 0, [2]float64{}, false
	}
	img, err := png.Decode(bytes.NewReader(f.png))
	if err != nil {
		return "", 0, [2]float64{}, false
	}
	ib := img.Bounds()
	px := image.Rect(
		ib.Min.X+int(math.Round(left*f.factor)), ib.Min.Y+int(math.Round(top*f.factor)),
		ib.Min.X+int(math.Round(right*f.factor)), ib.Min.Y+int(math.Round(bottom*f.factor)),
	).Intersect(ib)
	if px.Empty() {
		return "", 0, [2]float64{}, false
	}
	out := image.NewRGBA(image.Rect(0, 0, px.Dx(), px.Dy()))
	draw.Draw(out, out.Bounds(), img, px.Min, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return "", 0, [2]float64{}, false
	}
	off := [2]float64{round1(rx - (left+right)/2), round1(ry - (top+bottom)/2)}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), f.factor, off, true
}

func round1(v float64) float64 {
	r := math.Round(v*10) / 10
	if r == 0 {
		return 0 // no negative zero
	}
	return r
}

// windowOf finds a clicked element's window: by the window the tap read, or
// along its path, by key or by title within the element's process.
func windowOf(ws []view.RawWindow, h *Hit) *view.RawWindow {
	if h == nil {
		return nil
	}
	nodes := h.Path
	if h.Window != nil {
		nodes = append([]PathNode{*h.Window}, nodes...)
	}
	for _, n := range nodes {
		for i := range ws {
			w := &ws[i]
			if n.Key != "" {
				if w.Key == n.Key {
					return w
				}
				continue
			}
			if isWindowRole(n.Role) && w.Title == n.Name && h.PID > 0 && w.PID == h.PID {
				return w
			}
		}
	}
	return nil
}

func isWindowRole(r string) bool {
	switch r {
	case "window", "dialog", "sheet", "panel":
		return true
	}
	return false
}

func appOf(ws []view.RawWindow, pid int) string {
	if pid <= 0 {
		return ""
	}
	for _, w := range ws {
		if w.PID == pid && w.App != "" {
			return w.App
		}
	}
	return ""
}

func hasPID(ws []view.RawWindow, pid int) bool {
	for _, w := range ws {
		if w.PID == pid {
			return true
		}
	}
	return false
}

// frontOf is the active window, skipping an untitled plain window (a
// desktop or an app's invisible helper).
func frontOf(ws []view.RawWindow) *view.RawWindow {
	for i := range ws {
		w := ws[i]
		if w.Active && (strings.TrimSpace(w.Title) != "" || w.Role != "window") {
			return &w
		}
	}
	return nil
}
