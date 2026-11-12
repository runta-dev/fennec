package vision

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync/atomic"

	"fennec/cua/internal/protocol"
)

// DefaultEdge is a vision screenshot's long edge unless max_edge says
// otherwise.
const DefaultEdge = 1024

// Observer answers the parts of observe without pixels (view.Observer).
type Observer interface {
	Observe(ctx context.Context, req protocol.ObserveRequest) (*protocol.ObserveResult, error)
}

// Service adds captures to observe answers: image window, screen and
// region, and the screenshot with numbered elements of vision mode.
type Service struct {
	Capturer Capturer
	Sessions *Sessions
	Windows  *WindowScales
	// Resolve maps a window ref to its window server id.
	Resolve func(ref string) (id uint32, ok bool)

	oneShot atomic.Uint64
}

// NewService returns a service over a capturer.
func NewService(c Capturer, resolve func(ref string) (uint32, bool)) *Service {
	return &Service{Capturer: c, Sessions: NewSessions(), Windows: &WindowScales{}, Resolve: resolve}
}

// Image kinds of observe.
const (
	ImageNone   = "none"
	ImageWindow = "window"
	ImageScreen = "screen"
	ImageRegion = "region"
)

// SessionFor is the vision session of a caller; an empty agent has none.
func SessionFor(agent string) string {
	if agent == "" {
		return ""
	}
	return DefaultSession(agent)
}

// Observe answers an observe request that asks for pixels. agent is the
// caller's id; base answers the rest of the request.
func (s *Service) Observe(ctx context.Context, agent string, req protocol.ObserveRequest, base Observer) (*protocol.ObserveResult, error) {
	session := SessionFor(agent)
	image := req.Image
	if image == "" {
		image = ImageNone
	}
	switch image {
	case ImageNone, ImageWindow, ImageScreen, ImageRegion:
	default:
		return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: fmt.Sprintf("image %q is not none, window, screen or region", req.Image)}
	}
	if req.Vision && image != ImageNone {
		return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "vision takes its own screenshot; leave image out", Hint: "observe with vision: true, or with image alone"}
	}
	if image == ImageRegion && req.Region == nil {
		return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "image region needs region [x, y, w, h] in pixels of your latest screenshot"}
	}
	plain := req
	plain.Vision, plain.Image, plain.Region, plain.MaxEdge = false, "", nil, 0
	if req.Vision {
		if plain.Scope != "" && plain.Scope != protocol.ScopeWindow {
			return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "vision observes a window; leave scope out", Hint: "observe with vision: true and window to pick one"}
		}
		plain.Nodes = true
	}
	res, err := base.Observe(ctx, plain)
	if err != nil {
		return nil, err
	}
	switch {
	case req.Vision:
		return s.vision(ctx, session, req, res)
	case image == ImageScreen:
		return s.screen(ctx, req, res)
	case image == ImageWindow:
		return s.window(ctx, req, res)
	case image == ImageRegion:
		return s.region(ctx, session, req, res)
	}
	return res, nil
}

// captureError reports a failed capture with the driver's own reason.
func captureError(err error) error {
	var ce *CaptureError
	if errors.As(err, &ce) {
		return &protocol.Error{Code: protocol.ErrUnavailable, Message: ce.Error(), Hint: "wake the display or check Screen Recording permission, then observe again"}
	}
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe
	}
	return &protocol.Error{Code: protocol.ErrInternal, Message: err.Error()}
}

// desktop takes one full-size desktop capture in session (a one-shot
// session of its own when session is empty, ended afterwards).
func (s *Service) desktop(ctx context.Context, session string) (*Capture, error) {
	lane := session
	if lane == "" {
		lane = "shot-" + strconv.FormatUint(s.oneShot.Add(1), 10)
		defer s.Capturer.End(lane)
	}
	c, err := s.Capturer.Desktop(ctx, lane, 0)
	if err != nil {
		return nil, captureError(err)
	}
	return c, nil
}

// Screenshot takes a vision screenshot: one full-size capture in the
// caller's session, shrunk here so the driver never rescales a later
// desktop click, and kept as the session's map (and, unless shared, the
// latest any session falls back to).
func (s *Service) Screenshot(ctx context.Context, session, view string, maxEdge int, shared bool) (Screenshot, Shot, *Capture, error) {
	c, err := s.desktop(ctx, session)
	if err != nil {
		return Screenshot{}, Shot{}, nil, err
	}
	shot, sh, err := s.Adopt(session, view, c, maxEdge, shared)
	return shot, sh, c, err
}

// Adopt turns a capture (a fresh one, or the one a settle held still on)
// into the session's screenshot.
func (s *Service) Adopt(session, view string, c *Capture, maxEdge int, shared bool) (Screenshot, Shot, error) {
	edge := maxEdge
	switch {
	case edge == 0:
		edge = DefaultEdge
	case edge < 0:
		edge = 0
	}
	out, err := Prepare(c, edge, FormatAuto)
	if err != nil {
		return Screenshot{}, Shot{}, captureError(err)
	}
	native := c.DesktopScale()
	shot := Shot{View: view, Kind: KindScreen, Scale: native * out.Ratio, W: out.Image.W, H: out.Image.H}
	shot.Screen = [2]float64{float64(c.W) * native, float64(c.H) * native}
	s.Sessions.Keep(session, shot, shared)
	if session == "" {
		s.Sessions.End("")
	}
	return out, shot, nil
}

func (s *Service) vision(ctx context.Context, session string, req protocol.ObserveRequest, res *protocol.ObserveResult) (*protocol.ObserveResult, error) {
	shot, m, c, err := s.Screenshot(ctx, session, res.View, req.MaxEdge, false)
	if err != nil {
		return nil, err
	}
	els := ElementsOf(res.Nodes)
	s.Sessions.SetMarks(session, els)
	list, marks := ElementList(els, m)
	text := WindowLines(WindowsOf(res.Windows, s.Resolve), m, c.Backing)
	if res.Window != nil {
		text += fmt.Sprintf("\nElements of %s %s (center = screenshot pixels):\n", res.Window.Ref, fieldTitle(res.Window.Title))
	} else {
		text += "\nElements (center = screenshot pixels):\n"
	}
	if len(els) == 0 {
		list = "(none)\n"
	}
	res.Outline = text + list
	if !req.Nodes {
		res.Nodes = nil
	}
	res.Marks = marks
	res.Image = m.Image(shot.Image)
	res.Notes = append(res.Notes, "screenshot: "+Weigh(shot.Image.Data))
	return res, nil
}

func fieldTitle(t string) string { return strconv.Quote(t) }

// screen captures the main display, or a rectangle of it in desktop
// coordinates; the PNG is full size unless max_edge is set.
func (s *Service) screen(ctx context.Context, req protocol.ObserveRequest, res *protocol.ObserveResult) (*protocol.ObserveResult, error) {
	c, err := s.Capturer.Desktop(ctx, "", 0)
	if err != nil {
		return nil, captureError(err)
	}
	f, err := c.Pixels()
	if err != nil {
		return nil, captureError(err)
	}
	native := c.DesktopScale()
	origin := [2]float64{}
	if r := req.Region; r != nil {
		rect, err := UnitRect([4]float64{float64(r[0]), float64(r[1]), float64(r[2]), float64(r[3])}, 1/native, f.W, f.H)
		if err != nil {
			return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "the rectangle is off the main display", Hint: "to capture a window on another display, observe with image: window and name the window"}
		}
		if f, _, err = f.Crop(rect); err != nil {
			return nil, captureError(err)
		}
		origin = [2]float64{float64(rect.X) * native, float64(rect.Y) * native}
	}
	small := f
	if req.MaxEdge > 0 {
		small = Lanczos(f, req.MaxEdge)
	}
	enc, err := Encode(small, FormatPNG)
	if err != nil {
		return nil, captureError(err)
	}
	shot := Shot{View: res.View, Kind: KindScreen, Origin: origin, Scale: native * float64(f.W) / float64(small.W), W: small.W, H: small.H}
	s.Sessions.Record(shot)
	res.Image = shot.Image(enc)
	pw, ph := Screen{W: c.ScreenW, H: c.ScreenH, Backing: c.Backing}.Pixels()
	res.Notes = append(res.Notes, fmt.Sprintf("screen: %dx%d pixels at scale %s", pw, ph, strconv.FormatFloat(c.Backing, 'f', -1, 64)), "image: "+Weigh(enc.Data))
	return res, nil
}

// StaleNote explains a window image that may hold old pixels.
func StaleNote(w protocol.Window) string {
	why := "the window is minimized or hidden"
	if w.Space == "other" {
		why = "the window is on another Space"
	}
	app := w.App
	if app == "" {
		app = "the app"
	}
	return fmt.Sprintf("pixels may be stale: %s may repaint a window only while it is seen, and %s; an unchanged image does not prove that an action failed", app, why)
}

// window captures one window alone, on whichever display shows it, from
// its own state read (no accessibility tree, no desktop capture). Region
// is a crop in the window image's own pixels.
func (s *Service) window(ctx context.Context, req protocol.ObserveRequest, res *protocol.ObserveResult) (*protocol.ObserveResult, error) {
	w := res.Window
	if w == nil || s.Resolve == nil {
		return nil, &protocol.Error{Code: protocol.ErrWindowNotFound, Message: "no window to capture", Hint: "name the window"}
	}
	pid := w.PID
	id, ok := s.Resolve(w.Ref)
	if !ok || w.Bounds == nil {
		return nil, &protocol.Error{Code: protocol.ErrWindowNotFound, Message: "window " + w.Ref + " has no window server id or bounds to capture", Hint: "observe with image: screen instead"}
	}
	c, err := s.Capturer.Window(ctx, "", pid, id)
	if err != nil {
		return nil, captureError(err)
	}
	f, err := c.Pixels()
	if err != nil {
		return nil, captureError(err)
	}
	k, err := WindowScale(c, *w.Bounds)
	if err != nil {
		return nil, err
	}
	s.Windows.Prove(WindowTarget{PID: pid, ID: id, Bounds: *w.Bounds}.key(), k)
	crop := Rect{0, 0, f.W, f.H}
	if r := req.Region; r != nil {
		if f, crop, err = f.Crop(Rect{r[0], r[1], r[2], r[3]}); err != nil {
			return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "the rectangle is off the window", Hint: fmt.Sprintf("the window image is %dx%d pixels", c.W, c.H)}
		}
	}
	small := f
	if req.MaxEdge > 0 {
		small = Lanczos(f, req.MaxEdge)
	}
	enc, err := Encode(small, FormatPNG)
	if err != nil {
		return nil, captureError(err)
	}
	ratio := float64(f.W) / float64(small.W)
	shot := Shot{
		View:   res.View,
		Kind:   KindWindow,
		Origin: [2]float64{float64(w.Bounds[0]) + float64(crop.X)/k[0], float64(w.Bounds[1]) + float64(crop.Y)/k[1]},
		Scale:  ratio / k[0],
		W:      small.W,
		H:      small.H,
	}
	s.Sessions.Record(shot)
	res.Image = shot.Image(enc)
	res.Notes = append(res.Notes, fmt.Sprintf("crop origin in the window image: [%d,%d]", crop.X, crop.Y), "image: "+Weigh(enc.Data))
	if w.Space == "other" || w.Hidden != "" {
		res.Image.Stale = true
		res.Notes = append(res.Notes, StaleNote(*w))
	}
	return res, nil
}

// region zooms into the caller's latest screenshot (else the latest of
// any session): the rectangle in its pixels is cut from a full-size
// capture at native resolution, clipped at the image edge and shrunk by
// area averaging only when max_edge is smaller.
func (s *Service) region(ctx context.Context, session string, req protocol.ObserveRequest, res *protocol.ObserveResult) (*protocol.ObserveResult, error) {
	r := req.Region
	z, err := s.Zoom(ctx, session, res.View, [4]float64{float64(r[0]), float64(r[1]), float64(r[2]), float64(r[3])}, req.MaxEdge)
	if err != nil {
		return nil, err
	}
	res.Image = z.Shot.Image(z.Image)
	res.Notes = append(res.Notes, z.Note, "image: "+Weigh(z.Image.Data))
	return res, nil
}

// Zoomed is a region zoom.
type Zoomed struct {
	Image Encoded
	Shot  Shot
	// Factor maps a zoom pixel back to the screenshot act maps by:
	// x = Origin[0] + px*Factor.
	Origin [2]float64
	Factor float64
	Note   string
}

// Zoom cuts region, in pixels of the session's latest screenshot, from a
// full-size capture. A zoom without a session captures in a one-shot
// session of its own and ends it afterwards.
func (s *Service) Zoom(ctx context.Context, session, view string, region [4]float64, maxEdge int) (Zoomed, error) {
	base, ok := s.Sessions.Latest(session)
	if !ok {
		base = Shot{Kind: KindScreen, Scale: 1}
	}
	k := base.Scale
	area := [4]float64{base.Origin[0] + region[0]*k, base.Origin[1] + region[1]*k, region[2] * k, region[3] * k}
	c, err := s.desktop(ctx, session)
	if err != nil {
		return Zoomed{}, err
	}
	f, err := c.Pixels()
	if err != nil {
		return Zoomed{}, captureError(err)
	}
	perUnit := 1 / c.DesktopScale()
	rect, err := UnitRect(area, perUnit, f.W, f.H)
	if err != nil {
		return Zoomed{}, &protocol.Error{Code: protocol.ErrInvalid, Message: "the region is off the screen", Hint: "give x, y, w, h inside your latest screenshot"}
	}
	cut, _, err := f.Crop(rect)
	if err != nil {
		return Zoomed{}, captureError(err)
	}
	small := cut
	if maxEdge > 0 && maxEdge < max(cut.W, cut.H) {
		small = Fit(cut, maxEdge)
	}
	enc, err := Encode(small, FormatAuto)
	if err != nil {
		return Zoomed{}, captureError(err)
	}
	px := float64(cut.W) / float64(small.W) / perUnit // desktop units per zoom pixel
	shot := Shot{View: view, Kind: KindRegion, Origin: [2]float64{float64(rect.X) / perUnit, float64(rect.Y) / perUnit}, Scale: px, W: small.W, H: small.H}
	s.Sessions.Record(shot)
	ox, oy := base.FromDesktop(shot.Origin[0], shot.Origin[1])
	factor := px / k
	return Zoomed{
		Image:  enc,
		Shot:   shot,
		Origin: [2]float64{ox, oy},
		Factor: factor,
		Note: fmt.Sprintf("zoom pixel px,py is x = %.0f + px*%s, y = %.0f + py*%s in pixels of the screenshot act maps by",
			ox, strconv.FormatFloat(math.Round(factor*100)/100, 'f', -1, 64), oy, strconv.FormatFloat(math.Round(factor*100)/100, 'f', -1, 64)),
	}, nil
}
