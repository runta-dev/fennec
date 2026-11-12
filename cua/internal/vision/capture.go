package vision

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"fennec/cua/internal/driver"
)

// Capture is one capture as cua-driver answered it: the PNG and what the
// driver said about the screen or window it shows.
type Capture struct {
	PNG  []byte
	W, H int // image pixels
	// Desktop captures: the main screen in points, its backing scale and,
	// when the driver gives it, the points per image pixel.
	ScreenW, ScreenH float64
	Backing          float64
	FrameScale       float64
	// Window captures: the window's bounds in points.
	Bounds *[4]float64

	once  sync.Once
	frame *Frame
	err   error
}

// Pixels decodes the capture once.
func (c *Capture) Pixels() (*Frame, error) {
	c.once.Do(func() {
		if c.frame != nil {
			return
		}
		c.frame, c.err = DecodePNG(c.PNG)
	})
	return c.frame, c.err
}

// CaptureOf wraps raw pixels as a capture (tests and local captures).
func CaptureOf(f *Frame) *Capture {
	c := &Capture{W: f.W, H: f.H, frame: f}
	c.once.Do(func() {})
	return c
}

// PointsPerPixel is the capture's points per image pixel: the driver's
// frame scale, else the screen width divided by the screenshot width.
func (c *Capture) PointsPerPixel() float64 {
	if Positive(c.FrameScale) {
		return c.FrameScale
	}
	if c.ScreenW > 0 && c.W > 0 {
		return c.ScreenW / float64(c.W)
	}
	return 1
}

// DesktopScale is desktop coordinates (main display pixels) per capture
// pixel of a desktop capture: points per pixel times the backing scale.
func (c *Capture) DesktopScale() float64 {
	b := c.Backing
	if !Positive(b) {
		b = 1
	}
	return c.PointsPerPixel() * b
}

// Screen is the main display.
type Screen struct {
	W, H    float64 // points
	Backing float64
}

// Pixels is the screen's size in device pixels (desktop coordinates).
func (s Screen) Pixels() (int, int) {
	b := s.Backing
	if !Positive(b) {
		b = 1
	}
	return int(s.W*b + 0.5), int(s.H*b + 0.5)
}

// Capturer takes captures. Each session is a cua-driver session of its
// own, so a capture never disturbs another session's input state.
type Capturer interface {
	// Desktop captures the main display; edge caps the long edge (0 means
	// full size, which vision screenshots always ask for).
	Desktop(ctx context.Context, session string, edge int) (*Capture, error)
	// Window captures one window alone, on whichever display shows it.
	Window(ctx context.Context, session string, pid int, windowID uint32) (*Capture, error)
	// Screen reads the main display's size and backing scale.
	Screen(ctx context.Context, session string) (Screen, error)
	// End ends a session.
	End(session string)
}

// CaptureError is a failed capture with the driver's own reason.
type CaptureError struct {
	Reason string
}

func (e *CaptureError) Error() string { return "the capture failed: " + e.Reason }

// DriverCapturer implements Capturer over cua-driver: a session is a
// driver lane, and the empty session is the shared capture lane.
type DriverCapturer struct {
	Client *driver.Client
}

func lane(session string) string {
	if session == "" {
		return driver.LaneCapture
	}
	return session
}

// Desktop calls get_desktop_state.
func (d DriverCapturer) Desktop(ctx context.Context, session string, edge int) (*Capture, error) {
	raw, err := d.call(ctx, session, "get_desktop_state", map[string]any{"max_image_dimension": edge})
	if err != nil {
		return nil, err
	}
	return ParseDesktop(raw)
}

// Window calls get_window_state with the screenshot only.
func (d DriverCapturer) Window(ctx context.Context, session string, pid int, windowID uint32) (*Capture, error) {
	raw, err := d.call(ctx, session, "get_window_state", map[string]any{
		"pid":                        pid,
		"window_id":                  windowID,
		"include_accessibility_tree": false,
		"include_screenshot":         true,
		"max_image_dimension":        0,
	})
	if err != nil {
		return nil, err
	}
	return ParseWindow(raw)
}

// Screen calls get_screen_size.
func (d DriverCapturer) Screen(ctx context.Context, session string) (Screen, error) {
	raw, err := d.call(ctx, session, "get_screen_size", map[string]any{})
	if err != nil {
		return Screen{}, err
	}
	var r toolResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return Screen{}, &CaptureError{Reason: "undecodable reply: " + err.Error()}
	}
	var s struct {
		Width, Height float64
		Scale         float64 `json:"scale_factor"`
	}
	if err := json.Unmarshal(r.Structured, &s); err != nil {
		return Screen{}, &CaptureError{Reason: "undecodable screen size: " + err.Error()}
	}
	return Screen{W: s.Width, H: s.Height, Backing: s.Scale}, nil
}

// End drops the session's lane, which ends its driver session.
func (d DriverCapturer) End(session string) {
	if session != "" {
		d.Client.EndLane(session)
	}
}

func (d DriverCapturer) call(ctx context.Context, session, tool string, args map[string]any) (json.RawMessage, error) {
	raw, err := d.Client.Call(ctx, lane(session), tool, args)
	if err != nil {
		var de *driver.Error
		if errors.As(err, &de) {
			return nil, &CaptureError{Reason: de.Message}
		}
		return nil, &CaptureError{Reason: err.Error()}
	}
	return raw, nil
}

// toolResult is a driver tool's MCP-shaped reply.
type toolResult struct {
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	} `json:"content"`
	Structured json.RawMessage `json:"structuredContent"`
	IsError    bool            `json:"isError"`
}

func (r toolResult) text() string {
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func parseImage(raw json.RawMessage) (toolResult, []byte, error) {
	var r toolResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, nil, &CaptureError{Reason: "undecodable reply: " + err.Error()}
	}
	if r.IsError {
		reason := r.text()
		if reason == "" {
			reason = "the driver failed without a reason"
		}
		return r, nil, &CaptureError{Reason: reason}
	}
	for _, c := range r.Content {
		if c.Type == "image" && c.Data != "" {
			data, err := base64.StdEncoding.DecodeString(c.Data)
			if err != nil {
				return r, nil, &CaptureError{Reason: "undecodable image: " + err.Error()}
			}
			return r, data, nil
		}
	}
	return r, nil, &CaptureError{Reason: "the driver returned no image"}
}

// ParseDesktop reads a get_desktop_state reply.
func ParseDesktop(raw json.RawMessage) (*Capture, error) {
	r, data, err := parseImage(raw)
	if err != nil {
		return nil, err
	}
	var s struct {
		ShotW      float64 `json:"screenshot_width"`
		ShotH      float64 `json:"screenshot_height"`
		ScreenW    float64 `json:"screen_width"`
		ScreenH    float64 `json:"screen_height"`
		Backing    float64 `json:"scale_factor"`
		FrameScale float64 `json:"frame_scale"`
	}
	if len(r.Structured) > 0 {
		if err := json.Unmarshal(r.Structured, &s); err != nil {
			return nil, &CaptureError{Reason: "undecodable capture metadata: " + err.Error()}
		}
	}
	c := &Capture{PNG: data, W: int(s.ShotW), H: int(s.ShotH), ScreenW: s.ScreenW, ScreenH: s.ScreenH, Backing: s.Backing, FrameScale: s.FrameScale}
	return c, fillSize(c)
}

// ParseWindow reads a get_window_state reply with a screenshot.
func ParseWindow(raw json.RawMessage) (*Capture, error) {
	r, data, err := parseImage(raw)
	if err != nil {
		return nil, err
	}
	var s struct {
		ShotW  float64 `json:"screenshot_width"`
		ShotH  float64 `json:"screenshot_height"`
		Bounds *struct {
			X, Y, Width, Height float64
		} `json:"window_bounds"`
	}
	if len(r.Structured) > 0 {
		if err := json.Unmarshal(r.Structured, &s); err != nil {
			return nil, &CaptureError{Reason: "undecodable capture metadata: " + err.Error()}
		}
	}
	c := &Capture{PNG: data, W: int(s.ShotW), H: int(s.ShotH)}
	if s.Bounds != nil {
		c.Bounds = &[4]float64{s.Bounds.X, s.Bounds.Y, s.Bounds.Width, s.Bounds.Height}
	}
	return c, fillSize(c)
}

// fillSize takes the pixel size from the PNG header, which is the truth
// when the metadata lacks it or disagrees.
func fillSize(c *Capture) error {
	format, w, h, ok := ImageSize(c.PNG)
	if !ok || format != FormatPNG {
		return &CaptureError{Reason: fmt.Sprintf("the capture is not a PNG (%d bytes)", len(c.PNG))}
	}
	c.W, c.H = w, h
	return nil
}
