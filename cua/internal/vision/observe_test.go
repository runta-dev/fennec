package vision

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/protocol"
)

// retina is a 2880x1800 capture of a 1440x900-point screen at 2x with a
// red marker square at device pixels 2000..2040, 1000..1040.
func retina(t testing.TB) *Capture {
	retinaOnce.Do(func() {
		f := ui(2880, 1800)
		f.Fill(Rect{2000, 1000, 40, 40}, 255, 0, 0)
		retinaRaw = desktopReply(t, f, 1440, 900, 2, nil)
	})
	c, err := ParseDesktop(retinaRaw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var (
	retinaOnce sync.Once
	retinaRaw  json.RawMessage
)

// redCentre finds the red marker's centre in a frame.
func redCentre(f *Frame) (float64, float64, bool) {
	var sx, sy, n float64
	for y := 0; y < f.H; y++ {
		for x := 0; x < f.W; x++ {
			if r, g, b := f.RGB(x, y); r > 200 && g < 60 && b < 60 {
				sx, sy, n = sx+float64(x), sy+float64(y), n+1
			}
		}
	}
	return sx/n + 0.5, sy/n + 0.5, n > 0
}

func decodeImage(t *testing.T, img *protocol.Image) *Frame {
	t.Helper()
	data, err := decodeB64(img.Data)
	if err != nil {
		t.Fatal(err)
	}
	f, err := decodeAny(data)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func visionBase(nodes []protocol.Node) *fakeBase {
	return &fakeBase{res: func(req protocol.ObserveRequest) *protocol.ObserveResult {
		w := protocol.Window{Ref: "w1", App: "TextEdit", Title: "Draft", Role: "window", PID: 10, Active: true, Bounds: bounds(200, 100, 1600, 1000)}
		res := &protocol.ObserveResult{View: "v7", Window: &w, Windows: []protocol.Window{w}, Outline: "w1 window \"Draft\""}
		if req.Nodes {
			res.Nodes = nodes
		}
		return res
	}}
}

func TestScreenshotIsOneFullSizeCapture(t *testing.T) {
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) { return retina(t), nil }}
	svc := NewService(fc, func(string) (uint32, bool) { return 77, true })
	res, err := svc.Observe(context.Background(), "agent 1", protocol.ObserveRequest{Vision: true}, visionBase(nil))
	if err != nil {
		t.Fatal(err)
	}
	calls := fc.all()
	if len(calls) != 1 || calls[0].Kind != "desktop" || calls[0].Edge != 0 || calls[0].Session != "vision-agent-1" {
		t.Fatalf("captures %+v", calls)
	}
	if res.Image.Width != 1024 || res.Image.Height != 640 {
		t.Fatalf("screenshot %dx%d", res.Image.Width, res.Image.Height)
	}
}

func TestSingleCapture(t *testing.T) {
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) { return retina(t), nil }}
	svc := NewService(fc, nil)
	nodes := []protocol.Node{{Ref: "e1", Role: "button", Name: "OK", Bounds: bounds(1990, 990, 60, 60)}}
	res, err := svc.Observe(context.Background(), "a", protocol.ObserveRequest{Vision: true, MaxEdge: 512}, visionBase(nodes))
	if err != nil {
		t.Fatal(err)
	}
	if fc.count("desktop") != 1 || res.Image.Width != 512 {
		t.Fatalf("%d captures, %d wide", fc.count("desktop"), res.Image.Width)
	}
	if !strings.Contains(res.Notes[len(res.Notes)-1], "512x320 webp") {
		t.Fatalf("notes %v", res.Notes)
	}
	if res.Marks[0].Center != [2]float64{359, 181} || !strings.Contains(res.Outline, `1 button "OK" center=(359,181)`) {
		t.Fatalf("marks %+v\n%s", res.Marks, res.Outline)
	}
	if !strings.Contains(res.Outline, `- w1 pid=10 window_id=0 TextEdit "Draft" at (36,18) 284x178`) {
		t.Fatalf("%s", res.Outline)
	}
	if res.Nodes != nil {
		t.Fatal("structured nodes leaked without nodes: true")
	}
}

func TestScaleSurvivesDownsize(t *testing.T) {
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) {
		return desktopCapture(t, ui(2560, 1600), 1280, 800, 2), nil
	}}
	svc := NewService(fc, nil)
	if _, err := svc.Observe(context.Background(), "a", protocol.ObserveRequest{Vision: true}, visionBase(nil)); err != nil {
		t.Fatal(err)
	}
	shot, err := svc.Sessions.Pin(DefaultSession("a"), "", 0)
	if err != nil || shot.Scale != 2.5 {
		t.Fatalf("scale %v %v", shot.Scale, err)
	}
	if fc.count("desktop") != 1 {
		t.Fatal("a second capture was taken")
	}
	// The full-size capture maps 1:1.
	if _, err := svc.Observe(context.Background(), "a", protocol.ObserveRequest{Vision: true, MaxEdge: -1}, visionBase(nil)); err != nil {
		t.Fatal(err)
	}
	if shot, _ = svc.Sessions.Pin(DefaultSession("a"), "", 0); shot.Scale != 1 {
		t.Fatalf("full size scale %v", shot.Scale)
	}
}

func TestScaleFromScreenAndScreenshotWidths(t *testing.T) {
	if k := NativeScale(0, 1280, 2560, 2); k != 1 {
		t.Fatalf("Retina native %v", k)
	}
	if k := NativeScale(0, 1440, 1440, 1); k != 1 {
		t.Fatalf("1x native %v", k)
	}
	if k := NativeScale(0.75, 1280, 2560, 2); k != 1.5 {
		t.Fatalf("frame scale ignored: %v", k)
	}
	svc := NewService(&fakeCapturer{}, nil)
	_, shot, err := svc.Adopt("s", "v1", retina(t), 0, false)
	if err != nil || math.Abs(shot.Scale-2.8125) > 1e-9 {
		t.Fatalf("2880 px of 1440 points at 2x shrunk to 1024: %v %v", shot.Scale, err)
	}
}

func TestScaleFallbackByWidth(t *testing.T) {
	f := solid(64, 40, 0, 0, 0)
	c, err := ParseDesktop(desktopReply(t, f, 32, 20, 2, nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.PointsPerPixel() != 0.5 || c.DesktopScale() != 1 {
		t.Fatalf("by widths: %v %v", c.PointsPerPixel(), c.DesktopScale())
	}
	c, _ = ParseDesktop(desktopReply(t, f, 32, 20, 2, map[string]any{"frame_scale": 1.0}))
	if c.PointsPerPixel() != 1 || c.DesktopScale() != 2 {
		t.Fatalf("by frame scale: %v %v", c.PointsPerPixel(), c.DesktopScale())
	}
}

func TestRetinaClickLandsWhereShown(t *testing.T) {
	capt := retina(t)
	full, _ := capt.Pixels()
	wantX, wantY, _ := redCentre(full) // device pixels = desktop coordinates
	svc := NewService(&fakeCapturer{desktop: func(int, string) (*Capture, error) { return capt, nil }}, nil)
	land := func(shotFrame *Frame, shot Shot) {
		t.Helper()
		x, y, ok := redCentre(shotFrame)
		if !ok {
			t.Fatal("the marker is not in the image")
		}
		req, err := Route(PixelStep{Action: "click", X: fp(math.Floor(x)), Y: fp(math.Floor(y))}, shot, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		dx, dy := xy(req)
		if math.Abs(dx-wantX) > 4 || math.Abs(dy-wantY) > 4 {
			t.Fatalf("the click lands at %v,%v; the marker is at %v,%v", dx, dy, wantX, wantY)
		}
		if half := 2000.0 / 2; dx/2 < half {
			t.Fatalf("not converted by the backing scale: %v", dx)
		}
	}
	for _, edge := range []int{0, 512, -1} {
		sh, shot, err := svc.Adopt("s", "", capt, edge, false)
		if err != nil {
			t.Fatal(err)
		}
		land(sh.Frame, shot)
	}
	// After a region zoom around the marker, a zoom pixel maps through the
	// zoom's own view.
	if _, _, err := svc.Adopt("s", "v1", capt, 0, false); err != nil {
		t.Fatal(err)
	}
	z, err := svc.Zoom(context.Background(), "s", "v2", [4]float64{690, 340, 60, 40}, 0)
	if err != nil {
		t.Fatal(err)
	}
	zf := decodeEncoded(t, z.Image)
	pinned, err := svc.Sessions.Pin("s", "v2", 0)
	if err != nil {
		t.Fatal(err)
	}
	land(zf, pinned)
	// The formula the zoom gives maps to the same spot through the latest screenshot.
	x, y, _ := redCentre(zf)
	latest, _ := svc.Sessions.Pin("s", "", 0)
	dx, dy := latest.ToDesktop(z.Origin[0]+x*z.Factor, z.Origin[1]+y*z.Factor)
	if math.Abs(dx-wantX) > 4 || math.Abs(dy-wantY) > 4 {
		t.Fatalf("the zoom formula lands at %v,%v", dx, dy)
	}
}

func decodeEncoded(t *testing.T, e Encoded) *Frame {
	t.Helper()
	f, err := decodeAny(e.Data)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestZoomCutsFromFullCapture(t *testing.T) {
	capt := retina(t)
	full, _ := capt.Pixels()
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) { return capt, nil }}
	svc := NewService(fc, nil)
	svc.Sessions.Keep("s", Shot{Kind: KindScreen, Scale: 2.8125, Screen: [2]float64{2880, 1800}}, false)
	z, err := svc.Zoom(context.Background(), "s", "v3", [4]float64{700, 350, 32, 16}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c := fc.all(); len(c) != 1 || c[0].Edge != 0 || c[0].Session != "s" {
		t.Fatalf("captures %+v", c)
	}
	if z.Image.W != 90 || z.Image.H != 45 {
		t.Fatalf("zoom %dx%d, want native 90x45", z.Image.W, z.Image.H)
	}
	want, _, _ := full.Crop(Rect{1968, 984, 90, 45})
	sameFrame(t, decodeEncoded(t, z.Image), want)
	// A smaller max_edge shrinks by area averaging.
	if z, err = svc.Zoom(context.Background(), "s", "v4", [4]float64{700, 350, 32, 16}, 30); err != nil || z.Image.W != 30 || z.Image.H != 15 {
		t.Fatalf("shrunk %dx%d %v", z.Image.W, z.Image.H, err)
	}
	sameFrame(t, decodeEncoded(t, z.Image), Fit(want, 30))
	// Clipped at the edge; wholly outside fails.
	if z, err = svc.Zoom(context.Background(), "s", "", [4]float64{1000, 600, 100, 100}, 0); err != nil || z.Image.W != 2880-2812 {
		t.Fatalf("clipped %d %v", z.Image.W, err)
	}
	if _, err = svc.Zoom(context.Background(), "s", "", [4]float64{2000, 2000, 10, 10}, 0); err == nil || !strings.Contains(err.Error(), "off the screen") {
		t.Fatalf("outside: %v", err)
	}
}

func TestZoomMapsByLatestScreenshot(t *testing.T) {
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) { return desktopCapture(t, ui(2560, 1600), 1280, 800, 2), nil }}
	svc := NewService(fc, nil)
	svc.Sessions.Keep("other", Shot{Kind: KindScreen, Scale: 2.5}, false)
	// "mine" has no screenshot: the latest of any session is used.
	z, err := svc.Zoom(context.Background(), "mine", "", [4]float64{100, 40, 80, 60}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if z.Image.W != 200 || z.Image.H != 150 || z.Origin != [2]float64{100, 40} || z.Factor != 0.4 {
		t.Fatalf("%dx%d origin %v factor %v", z.Image.W, z.Image.H, z.Origin, z.Factor)
	}
	if !strings.Contains(z.Note, "x = 100 + px*0.4, y = 40 + py*0.4") {
		t.Fatalf("%q", z.Note)
	}
	svc.Sessions.Keep("mine", Shot{Kind: KindScreen, Scale: 1}, false)
	if z, _ = svc.Zoom(context.Background(), "mine", "", [4]float64{100, 40, 80, 60}, 0); z.Image.W != 80 || z.Factor != 1 {
		t.Fatalf("the caller's own screenshot was not used: %d %v", z.Image.W, z.Factor)
	}
}

func TestZoomOneShotSessionEnded(t *testing.T) {
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) { return desktopCapture(t, ui(640, 400), 640, 400, 1), nil }}
	svc := NewService(fc, nil)
	base := &fakeBase{res: func(protocol.ObserveRequest) *protocol.ObserveResult { return &protocol.ObserveResult{View: "v1"} }}
	res, err := svc.Observe(context.Background(), "", protocol.ObserveRequest{Image: ImageRegion, Region: &[4]int{10, 10, 50, 40}}, base)
	if err != nil {
		t.Fatal(err)
	}
	calls := fc.all()
	if len(calls) != 1 || !strings.HasPrefix(calls[0].Session, "shot-") || len(fc.ended) != 1 || fc.ended[0] != calls[0].Session {
		t.Fatalf("captures %+v ended %v", calls, fc.ended)
	}
	if res.Image.Width != 50 || res.Image.Origin != [2]float64{10, 10} {
		t.Fatalf("%+v", res.Image)
	}
}

func TestEmptyStateClearsElements(t *testing.T) {
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) { return desktopCapture(t, ui(640, 400), 640, 400, 1), nil }}
	svc := NewService(fc, nil)
	s := DefaultSession("a")
	if _, err := svc.Observe(context.Background(), "a", protocol.ObserveRequest{Vision: true}, visionBase([]protocol.Node{{Ref: "e1", Role: "button", Name: "OK"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sessions.Mark(s, 1); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Observe(context.Background(), "a", protocol.ObserveRequest{Vision: true}, visionBase(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sessions.Mark(s, 1); err == nil {
		t.Fatal("an empty vision observe kept the previous numbers")
	}
	if !strings.Contains(res.Outline, "(none)") || len(res.Marks) != 0 {
		t.Fatalf("%s", res.Outline)
	}
}

func TestFailedCaptureCarriesDriverReason(t *testing.T) {
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) {
		return nil, &CaptureError{Reason: "Desktop screenshot failed: the display is asleep"}
	}}
	svc := NewService(fc, nil)
	_, err := svc.Observe(context.Background(), "a", protocol.ObserveRequest{Vision: true}, visionBase(nil))
	var pe *protocol.Error
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, "the display is asleep") {
		t.Fatalf("%v", err)
	}
	// The driver's isError reply carries its text as the reason.
	raw, _ := json.Marshal(map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "Desktop screenshot failed: display asleep"}}})
	if _, err := ParseDesktop(raw); err == nil || !strings.Contains(err.Error(), "display asleep") {
		t.Fatalf("%v", err)
	}
	client, _ := fakeDriver(t, func(string, map[string]any) string { return `{"ok":false,"error":"screen capture is not permitted"}` })
	_, err = DriverCapturer{Client: client}.Desktop(context.Background(), "s", 0)
	if err == nil || !strings.Contains(err.Error(), "screen capture is not permitted") {
		t.Fatalf("%v", err)
	}
}

func windowBase(w protocol.Window) *fakeBase {
	return &fakeBase{res: func(protocol.ObserveRequest) *protocol.ObserveResult {
		return &protocol.ObserveResult{View: "v3", Window: &w, Windows: []protocol.Window{w}}
	}}
}

func TestWindowShotStaleMark(t *testing.T) {
	fc := &fakeCapturer{window: func(int, uint32) (*Capture, error) {
		return windowCapture(t, ui(400, 300), [4]float64{0, 0, 400, 300}), nil
	}}
	svc := NewService(fc, func(string) (uint32, bool) { return 5, true })
	for _, c := range []struct {
		w     protocol.Window
		stale string
	}{
		{protocol.Window{Ref: "w1", App: "TextEdit", Title: "A", Space: "other", Bounds: bounds(0, 0, 400, 300)}, "on another Space"},
		{protocol.Window{Ref: "w1", App: "TextEdit", Title: "A", Hidden: "minimized", Bounds: bounds(0, 0, 400, 300)}, "minimized or hidden"},
		{protocol.Window{Ref: "w1", App: "TextEdit", Title: "A", Bounds: bounds(0, 0, 400, 300)}, ""}, // merely covered
	} {
		res, err := svc.Observe(context.Background(), "a", protocol.ObserveRequest{Image: ImageWindow}, windowBase(c.w))
		if err != nil {
			t.Fatal(err)
		}
		notes := strings.Join(res.Notes, "\n")
		if c.stale == "" {
			if res.Image.Stale || strings.Contains(notes, "stale") {
				t.Fatalf("a covered window was marked stale: %v", notes)
			}
			continue
		}
		if !res.Image.Stale || !strings.Contains(notes, "pixels may be stale: TextEdit") || !strings.Contains(notes, c.stale) || !strings.Contains(notes, "does not prove that an action failed") {
			t.Fatalf("%+v %s", res.Image.Stale, notes)
		}
	}
}

func TestWindowShotAnyDisplay(t *testing.T) {
	// A window on a 1x display left of a 2x main display: its bounds are
	// desktop coordinates (main pixels), its image is 1x.
	fc := &fakeCapturer{window: func(pid int, id uint32) (*Capture, error) {
		return windowCapture(t, ui(800, 500), [4]float64{-800, 100, 800, 500}), nil
	}}
	svc := NewService(fc, func(ref string) (uint32, bool) { return 4242, ref == "w5" })
	w := protocol.Window{Ref: "w5", PID: 321, App: "Safari", Title: "Docs", Bounds: bounds(-1600, 200, 1600, 1000)}
	res, err := svc.Observe(context.Background(), "a", protocol.ObserveRequest{Image: ImageWindow, Window: "w5"}, windowBase(w))
	if err != nil {
		t.Fatal(err)
	}
	calls := fc.all()
	if len(calls) != 1 || calls[0].Kind != "window" || calls[0].PID != 321 || calls[0].ID != 4242 {
		t.Fatalf("captures %+v: a window shot reads that window alone", calls)
	}
	img := res.Image
	if img.Width != 800 || img.Origin != [2]float64{-1600, 200} || img.Scale != 2 || img.MIME != "image/png" {
		t.Fatalf("%+v", img)
	}
	if shot, ok := svc.Sessions.View("v3"); !ok || shot.Kind != KindWindow {
		t.Fatal("the window shot is not pinned to its view")
	}
	if _, err := svc.Sessions.Pin(DefaultSession("a"), "", 0); err == nil {
		t.Fatal("a window shot became the session's vision screenshot")
	}
}

func TestWindowShotCropOrigin(t *testing.T) {
	f := ui(400, 300)
	f.Set(30, 20, 1, 2, 3)
	fc := &fakeCapturer{window: func(int, uint32) (*Capture, error) { return windowCapture(t, f, [4]float64{100, 100, 200, 150}), nil }}
	svc := NewService(fc, func(string) (uint32, bool) { return 1, true })
	w := protocol.Window{Ref: "w1", PID: 1, Title: "A", Bounds: bounds(200, 200, 400, 300)}
	res, err := svc.Observe(context.Background(), "a", protocol.ObserveRequest{Image: ImageWindow, Region: &[4]int{30, 20, 50, 40}}, windowBase(w))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "crop origin in the window image: [30,20]") {
		t.Fatalf("%v", res.Notes)
	}
	got := decodeImage(t, res.Image)
	if r, g, b := got.RGB(0, 0); r != 1 || g != 2 || b != 3 || got.W != 50 || got.H != 40 {
		t.Fatal("the crop does not start at its origin")
	}
	if res.Image.Origin != [2]float64{230, 220} {
		t.Fatalf("desktop origin %v", res.Image.Origin)
	}
	res, _ = svc.Observe(context.Background(), "a", protocol.ObserveRequest{Image: ImageWindow}, windowBase(w))
	if !strings.Contains(strings.Join(res.Notes, "\n"), "crop origin in the window image: [0,0]") {
		t.Fatalf("%v", res.Notes)
	}
	if _, err := svc.Observe(context.Background(), "a", protocol.ObserveRequest{Image: ImageWindow, Region: &[4]int{500, 0, 10, 10}}, windowBase(w)); err == nil || !strings.Contains(err.Error(), "off the window") {
		t.Fatalf("%v", err)
	}
}

func TestScreenSizeInPixels(t *testing.T) {
	if w, h := (Screen{W: 1280, H: 800, Backing: 2}).Pixels(); w != 2560 || h != 1600 {
		t.Fatalf("%dx%d", w, h)
	}
	client, log := fakeDriver(t, func(tool string, _ map[string]any) string {
		return okReply(json.RawMessage(`{"content":[{"type":"text","text":"1280x800"}],"structuredContent":{"width":1280,"height":800,"scale_factor":2}}`))
	})
	s, err := DriverCapturer{Client: client}.Screen(context.Background(), "")
	if err != nil || s.Backing != 2 {
		t.Fatalf("%+v %v", s, err)
	}
	if w, h := s.Pixels(); w != 2560 || h != 1600 {
		t.Fatalf("%dx%d", w, h)
	}
	if log.reqs[0].Tool != "get_screen_size" {
		t.Fatalf("%+v", log.reqs)
	}
}

func TestRetinaScreenPixels(t *testing.T) {
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) { return retina(t), nil }}
	svc := NewService(fc, nil)
	base := &fakeBase{res: func(protocol.ObserveRequest) *protocol.ObserveResult { return &protocol.ObserveResult{View: "v1"} }}
	res, err := svc.Observe(context.Background(), "a", protocol.ObserveRequest{Image: ImageScreen}, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "screen: 2880x1800 pixels at scale 2") {
		t.Fatalf("%v", res.Notes)
	}
	if res.Image.Width != 2880 || res.Image.Scale != 1 || res.Image.MIME != "image/png" {
		t.Fatalf("%+v", res.Image)
	}
	// A rectangle in desktop coordinates crops 1:1 on a native capture.
	res, err = svc.Observe(context.Background(), "a", protocol.ObserveRequest{Image: ImageScreen, Region: &[4]int{1990, 990, 60, 60}}, base)
	if err != nil || res.Image.Width != 60 || res.Image.Origin != [2]float64{1990, 990} {
		t.Fatalf("%+v %v", res.Image, err)
	}
	if _, err = svc.Observe(context.Background(), "a", protocol.ObserveRequest{Image: ImageScreen, Region: &[4]int{-500, 0, 100, 100}}, base); err == nil || !strings.Contains(err.Error(), "off the main display") {
		t.Fatalf("%v", err)
	}
}

func TestCaptureLatencyBudget(t *testing.T) {
	// A whole-desktop capture is one driver call (cua-driver captures with
	// ScreenCaptureKit; nothing is spawned), at full size.
	reply := okReply(desktopReply(t, ui(2560, 1600), 1280, 800, 2, nil))
	client, log := fakeDriver(t, func(string, map[string]any) string { return reply })
	start := time.Now()
	c, err := DriverCapturer{Client: client}.Desktop(context.Background(), "vision-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(log.reqs) != 1 || log.reqs[0].Tool != "get_desktop_state" || log.reqs[0].Args["max_image_dimension"] != 0.0 {
		t.Fatalf("%+v", log.reqs)
	}
	// The local work of a 1024-edge screenshot stays inside the budget the
	// capture path is measured against.
	shot, err := Prepare(c, DefaultEdge, FormatAuto)
	if err != nil || shot.Image.W != 1024 {
		t.Fatal(err)
	}
	if took := time.Since(start); !raceEnabled && took > 325*time.Millisecond {
		t.Fatalf("decode, shrink and encode took %v", took)
	}
}

func TestGroundedCheckFreshCaptures(t *testing.T) {
	fc := &fakeCapturer{desktop: func(n int, _ string) (*Capture, error) {
		f := solid(1440, 900, uint8(n*60), 0, 0) // each capture differs
		return desktopCapture(t, f, 1440, 900, 1), nil
	}}
	g := GroundCheck{Capturer: fc, Session: "check"}
	closeUp, err := g.CloseUp(context.Background(), 700, 400)
	if err != nil {
		t.Fatal(err)
	}
	zoom, ring, err := g.Full(context.Background(), 700, 400, &[4]float64{680, 390, 40, 20})
	if err != nil {
		t.Fatal(err)
	}
	if fc.count("desktop") != 2 {
		t.Fatalf("%d captures, want one crop capture and one full capture", fc.count("desktop"))
	}
	if r, _, _ := decodeEncoded(t, closeUp.Image).RGB(0, 0); r != 60 {
		t.Fatal("the crop is not from its own capture")
	}
	if r, _, _ := decodeEncoded(t, zoom.Image).RGB(0, 0); r != 120 {
		t.Fatal("the closeUp-up is not from the full capture")
	}
	rf := decodeEncoded(t, ring.Image)
	if r, _, _ := rf.RGB(10, 10); r != 120 {
		t.Fatal("the ringed image is not from the full capture")
	}
	if closeUp.Prompt != "A 640x400 crop of the screen. A click is about to land on its pixel (320, 200)." {
		t.Fatalf("%q", closeUp.Prompt)
	}
	if zoom.Image.W < ZoomEdge {
		t.Fatalf("closeUp-up %d wide", zoom.Image.W)
	}
}

func TestGroundedCheckRetinaPixels(t *testing.T) {
	capt := retina(t)
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) { return capt, nil }}
	g := GroundCheck{Capturer: fc}
	closeUp, err := g.CloseUp(context.Background(), 2020, 1020)
	if err != nil {
		t.Fatal(err)
	}
	if closeUp.Image.W != 1280 || closeUp.Image.H != 800 || closeUp.Pixel != [2]int{640, 400} {
		t.Fatalf("crop %dx%d pixel %v", closeUp.Image.W, closeUp.Image.H, closeUp.Pixel)
	}
	cf := decodeEncoded(t, closeUp.Image)
	if r, g, _ := cf.RGB(640, 400); r != 255 || g != 0 {
		t.Fatal("the crop's stated pixel is not the point")
	}
	_, ring, err := g.Full(context.Background(), 2020, 1020, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ring.Prompt != "The screen, 2880x1800. A red ring with four ticks marks the spot a click is about to land on, centred at pixel (2020, 1020)." {
		t.Fatalf("%q", ring.Prompt)
	}
	rf := decodeEncoded(t, ring.Image)
	if r, g, b := rf.RGB(2020+RingPoints*2, 1020); r != 255 || g != 0 || b != 0 {
		t.Fatal("the ring is not at the backing-scaled radius")
	}
}

func TestCropRelativeToOrigin(t *testing.T) {
	margin := [2]float64{40, 4}
	// A control on a display left of the main one, captured with its origin.
	r := StateCrop([4]float64{-1800, 100, 50, 20}, [2]float64{-1920, 0}, 1, margin)
	if r != (Rect{80, 96, 130, 28}) {
		t.Fatalf("%+v", r)
	}
	// Above the main display, on a 2x capture of a 1x desktop unit.
	r = StateCrop([4]float64{100, -1000, 50, 20}, [2]float64{0, -1080}, 2, margin)
	if r != (Rect{120, 152, 260, 56}) {
		t.Fatalf("%+v", r)
	}
	// At the captured screen's corner the crop is cut, not shifted.
	r = StateCrop([4]float64{10, 2, 50, 20}, [2]float64{0, 0}, 1, margin)
	if r != (Rect{0, 0, 100, 26}) {
		t.Fatalf("%+v", r)
	}
}
