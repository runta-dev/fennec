package vision

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"fennec/cua/internal/protocol"
)

// Shot is how one image maps to the desktop: desktop = origin + pixel *
// scale, in desktop coordinates (main display pixels).
type Shot struct {
	View   string
	Kind   string // screen, window, region
	Origin [2]float64
	Scale  float64
	W, H   int
	// Screen is the main display's size in desktop coordinates, for the
	// off-screen check; zero when unknown.
	Screen [2]float64
}

// Shot kinds.
const (
	KindScreen = "screen"
	KindWindow = "window"
	KindRegion = "region"
)

// ToDesktop maps an image pixel to desktop coordinates.
func (s Shot) ToDesktop(x, y float64) (float64, float64) {
	return s.Origin[0] + x*s.Scale, s.Origin[1] + y*s.Scale
}

// FromDesktop maps desktop coordinates to image pixels.
func (s Shot) FromDesktop(x, y float64) (float64, float64) {
	return (x - s.Origin[0]) / s.Scale, (y - s.Origin[1]) / s.Scale
}

// Image is the protocol's description of the shot's map.
func (s Shot) Image(e Encoded) *protocol.Image {
	return &protocol.Image{MIME: e.MIME(), Data: e.Base64(), Width: e.W, Height: e.H, Origin: s.Origin, Scale: s.Scale}
}

// Positive says whether a scale is usable: finite and above zero.
func Positive(k float64) bool { return k > 0 && !math.IsInf(k, 0) && !math.IsNaN(k) }

// CheckScale refuses a scale that is zero, negative or not finite.
func CheckScale(k float64) error {
	if Positive(k) {
		return nil
	}
	return &protocol.Error{Code: protocol.ErrInvalid, Message: fmt.Sprintf("scale %v is not a positive number", k), Hint: "leave scale out to map by the latest screenshot"}
}

// NativeScale is desktop coordinates per pixel of a desktop capture: the
// driver's frame scale (points per pixel), else the screen width over the
// screenshot width, times the backing scale.
func NativeScale(frameScale, screenW float64, shotW int, backing float64) float64 {
	c := Capture{FrameScale: frameScale, ScreenW: screenW, W: shotW, Backing: backing}
	return c.DesktopScale()
}

// DefaultSession names a caller's own vision session from its client id,
// with every character that is not an ASCII letter or digit replaced.
func DefaultSession(agentID string) string {
	if agentID == "" {
		return "vision"
	}
	return "vision-" + strings.Map(func(r rune) rune {
		if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, agentID)
}

// maxViews bounds how many views keep their shot for pinned points.
const maxViews = 64

// Sessions keeps vision state per caller session: the latest screenshot's
// map, the numbered elements, and the shots of recent views so that a
// point pinned to a view maps by that view's screenshot whatever was
// captured since.
type Sessions struct {
	mu     sync.Mutex
	shots  map[string]Shot
	latest *Shot
	views  map[string]Shot
	order  []string
	marks  map[string][]Element
}

// NewSessions returns empty state.
func NewSessions() *Sessions {
	return &Sessions{shots: map[string]Shot{}, views: map[string]Shot{}, marks: map[string][]Element{}}
}

// Keep records a session's new screenshot. A shared (read-only) screenshot
// sets only its own session's map and never becomes the latest that other
// sessions fall back to.
func (s *Sessions) Keep(session string, shot Shot, shared bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shots[session] = shot
	if !shared {
		latest := shot
		s.latest = &latest
	}
	s.viewLocked(shot)
}

// Record pins a shot to its view without changing any session's latest
// (window shots and zooms).
func (s *Sessions) Record(shot Shot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.viewLocked(shot)
}

func (s *Sessions) viewLocked(shot Shot) {
	if shot.View == "" {
		return
	}
	if _, ok := s.views[shot.View]; !ok {
		s.order = append(s.order, shot.View)
	}
	s.views[shot.View] = shot
	for len(s.order) > maxViews {
		delete(s.views, s.order[0])
		s.order = s.order[1:]
	}
}

// Latest is the session's latest screenshot, else the latest of any
// session (one-shot screenshots included).
func (s *Sessions) Latest(session string) (Shot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if shot, ok := s.shots[session]; ok {
		return shot, true
	}
	if s.latest != nil {
		return *s.latest, true
	}
	return Shot{}, false
}

// View is the shot a view's image maps by.
func (s *Sessions) View(view string) (Shot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	shot, ok := s.views[view]
	return shot, ok
}

// End forgets a session's screenshot and elements; it then falls back to
// the latest of any session.
func (s *Sessions) End(session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.shots, session)
	delete(s.marks, session)
}

// SetMarks replaces a session's numbered elements; an empty list clears them.
func (s *Sessions) SetMarks(session string, els []Element) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(els) == 0 {
		delete(s.marks, session)
		return
	}
	s.marks[session] = els
}

// Mark finds element n of the session's latest vision observe.
func (s *Sessions) Mark(session string, n int) (Element, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	els, ok := s.marks[session]
	if !ok {
		return Element{}, &protocol.Error{Code: protocol.ErrTargetNotFound, Message: "no numbered elements yet in this session", Hint: "observe with vision: true first"}
	}
	for _, e := range els {
		if e.N == n {
			return e, nil
		}
	}
	return Element{}, &protocol.Error{Code: protocol.ErrTargetNotFound, Message: fmt.Sprintf("element %d is not in the latest vision observe", n), Hint: "observe with vision: true again"}
}

// Pin resolves the shot an act maps image points by, taken once when the
// act starts so a capture during it cannot move its points: the view a
// point names, else the session's latest, else the latest of any session;
// an explicit scale replaces the shot's scale.
func (s *Sessions) Pin(session, view string, scale float64) (Shot, error) {
	if scale != 0 {
		if err := CheckScale(scale); err != nil {
			return Shot{}, err
		}
	}
	var (
		shot Shot
		ok   bool
	)
	if view != "" {
		if shot, ok = s.View(view); !ok {
			return Shot{}, &protocol.Error{Code: protocol.ErrStaleRef, Message: "view " + view + " has no screenshot that is still kept", Hint: "observe with vision: true and use the new view"}
		}
	} else if shot, ok = s.Latest(session); !ok {
		if scale == 0 {
			return Shot{}, &protocol.Error{Code: protocol.ErrInvalid, Message: "no screenshot yet to map image points by", Hint: "observe with vision: true first"}
		}
		shot = Shot{Kind: KindScreen}
	}
	if scale != 0 {
		shot.Scale = scale
	}
	return shot, nil
}

// WindowKey identifies a window read: the same window at the same bounds
// under the same display layout keeps its capture scale.
type WindowKey struct {
	PID    int
	ID     uint32
	Bounds [4]int
	Layout string
}

// WindowScales remembers capture scales proven by window reads.
type WindowScales struct {
	mu sync.Mutex
	m  map[WindowKey][2]float64
}

// maxWindowScales bounds the cache.
const maxWindowScales = 32

// Prove records image pixels per desktop unit, across and down.
func (w *WindowScales) Prove(k WindowKey, scale [2]float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.m == nil || len(w.m) >= maxWindowScales {
		w.m = map[WindowKey][2]float64{}
	}
	w.m[k] = scale
}

// Proven returns a scale proven for exactly this key.
func (w *WindowScales) Proven(k WindowKey) ([2]float64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.m[k]
	return s, ok
}
