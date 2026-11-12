package vision

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math/rand"
	"net"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/driver"
	"fennec/cua/internal/protocol"

	_ "golang.org/x/image/webp"
)

// fakeClock advances only when slept on.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d > 0 {
		c.t = c.t.Add(d)
	}
	return ctx.Err()
}

func (c *fakeClock) since(t time.Time) time.Duration { return c.Now().Sub(t) }

// solid is a frame of one colour.
func solid(w, h int, r, g, b uint8) *Frame {
	f := NewFrame(w, h)
	f.Fill(Rect{0, 0, w, h}, r, g, b)
	return f
}

// noise is a frame of random pixels.
func noise(w, h int, seed int64) *Frame {
	f := NewFrame(w, h)
	rand.New(rand.NewSource(seed)).Read(f.Pix)
	return f
}

// ui is a flat, UI-like frame: a background, a title bar and buttons.
func ui(w, h int) *Frame {
	f := solid(w, h, 236, 236, 236)
	f.Fill(Rect{0, 0, w, h / 20}, 200, 200, 205)
	for i := 0; i < 8; i++ {
		f.Fill(Rect{w/10 + i*w/10, h / 3, w / 14, h / 16}, 0, 122, 255)
		f.Fill(Rect{w/10 + i*w/10 + 4, h/3 + 4, w/14 - 8, 3}, 255, 255, 255)
	}
	return f
}

func mustPNG(t testing.TB, f *Frame) []byte {
	t.Helper()
	data, err := EncodePNG(f)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// desktopReply is a get_desktop_state reply for a frame.
func desktopReply(t testing.TB, f *Frame, screenW, screenH, backing float64, extra map[string]any) json.RawMessage {
	t.Helper()
	s := map[string]any{
		"platform": "macos", "display": "primary",
		"screenshot_width": f.W, "screenshot_height": f.H,
		"screen_width": screenW, "screen_height": screenH,
		"scale_factor": backing, "screenshot_mime_type": "image/png",
	}
	for k, v := range extra {
		s[k] = v
	}
	raw, _ := json.Marshal(map[string]any{
		"content":           []any{map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(mustPNG(t, f)), "mimeType": "image/png"}, map[string]any{"type": "text", "text": "desktop screenshot"}},
		"structuredContent": s,
	})
	return raw
}

// desktopCapture is a parsed desktop capture of f.
func desktopCapture(t testing.TB, f *Frame, screenW, screenH, backing float64) *Capture {
	t.Helper()
	c, err := ParseDesktop(desktopReply(t, f, screenW, screenH, backing, nil))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// windowCapture is a parsed window capture of f.
func windowCapture(t testing.TB, f *Frame, bounds [4]float64) *Capture {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"content":           []any{map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(mustPNG(t, f)), "mimeType": "image/png"}},
		"structuredContent": map[string]any{"pid": 1, "window_id": 2, "screenshot_width": f.W, "screenshot_height": f.H, "window_bounds": map[string]any{"x": bounds[0], "y": bounds[1], "width": bounds[2], "height": bounds[3]}},
	})
	c, err := ParseWindow(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type capCall struct {
	Kind    string // desktop, window, screen
	Session string
	Edge    int
	PID     int
	ID      uint32
}

// fakeCapturer records calls and answers from functions.
type fakeCapturer struct {
	mu      sync.Mutex
	calls   []capCall
	ended   []string
	desktop func(n int, session string) (*Capture, error)
	window  func(pid int, id uint32) (*Capture, error)
	screen  Screen
}

func (f *fakeCapturer) Desktop(_ context.Context, session string, edge int) (*Capture, error) {
	f.mu.Lock()
	f.calls = append(f.calls, capCall{Kind: "desktop", Session: session, Edge: edge})
	n := len(f.calls)
	f.mu.Unlock()
	if f.desktop == nil {
		return nil, &CaptureError{Reason: "no desktop in this test"}
	}
	return f.desktop(n, session)
}

func (f *fakeCapturer) Window(_ context.Context, session string, pid int, id uint32) (*Capture, error) {
	f.mu.Lock()
	f.calls = append(f.calls, capCall{Kind: "window", Session: session, PID: pid, ID: id})
	f.mu.Unlock()
	if f.window == nil {
		return nil, &CaptureError{Reason: "no window in this test"}
	}
	return f.window(pid, id)
}

func (f *fakeCapturer) Screen(_ context.Context, session string) (Screen, error) {
	f.mu.Lock()
	f.calls = append(f.calls, capCall{Kind: "screen", Session: session})
	f.mu.Unlock()
	return f.screen, nil
}

func (f *fakeCapturer) End(session string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended = append(f.ended, session)
}

func (f *fakeCapturer) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

func (f *fakeCapturer) all() []capCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]capCall(nil), f.calls...)
}

// fakeBase answers the view part of observe.
type fakeBase struct {
	mu   sync.Mutex
	res  func(req protocol.ObserveRequest) *protocol.ObserveResult
	reqs []protocol.ObserveRequest
}

func (b *fakeBase) Observe(_ context.Context, req protocol.ObserveRequest) (*protocol.ObserveResult, error) {
	b.mu.Lock()
	b.reqs = append(b.reqs, req)
	b.mu.Unlock()
	return b.res(req), nil
}

func bounds(x, y, w, h int) *[4]int { return &[4]int{x, y, w, h} }

func str(s string) *string { return &s }

func fp(v float64) *float64 { return &v }

// fakeDriverClient answers driver calls over in-memory pipes and records
// the requests (tool and args) it saw.
type driverLog struct {
	mu   sync.Mutex
	reqs []struct {
		Lane, Tool string
		Args       map[string]any
	}
}

func fakeDriver(t *testing.T, answer func(tool string, args map[string]any) string) (*driver.Client, *driverLog) {
	t.Helper()
	log := &driverLog{}
	c := &driver.Client{Socket: "fake"}
	c.Dial = func(ctx context.Context, _ string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			r := bufio.NewReader(b)
			for {
				line, err := r.ReadBytes('\n')
				if err != nil {
					return
				}
				var req struct {
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				}
				_ = json.Unmarshal(line, &req)
				log.mu.Lock()
				log.reqs = append(log.reqs, struct {
					Lane, Tool string
					Args       map[string]any
				}{Tool: req.Name, Args: req.Args})
				log.mu.Unlock()
				if _, err := fmt.Fprintln(b, answer(req.Name, req.Args)); err != nil {
					return
				}
			}
		}()
		return a, nil
	}
	t.Cleanup(c.Close)
	return c, log
}

func okReply(result json.RawMessage) string {
	raw, _ := json.Marshal(map[string]any{"ok": true, "result": result})
	return string(raw)
}

func decodeB64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// decodeAny decodes a PNG, WebP or JPEG image into a frame.
func decodeAny(data []byte) (*Frame, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return FrameOf(img), nil
}
