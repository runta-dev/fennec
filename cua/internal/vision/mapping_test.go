package vision

import (
	"context"
	"errors"
	"testing"

	"fennec/cua/internal/protocol"
)

func TestWindowPixelsMapAcrossScales(t *testing.T) {
	cases := []struct {
		name   string
		bounds [4]int // desktop coordinates (main display pixels, main at 2x)
		img    [2]int // the window image's pixels
		px     [2]float64
		want   [2]float64
	}{
		// A window on the 2x main display: its image is 2x, so 1:1 with desktop pixels.
		{"main 2x", [4]int{200, 100, 800, 600}, [2]int{800, 600}, [2]float64{100, 50}, [2]float64{300, 150}},
		// A window on a 1x display left of the main one: one image pixel is two desktop pixels.
		{"left 1x", [4]int{-1600, 200, 1600, 1000}, [2]int{800, 500}, [2]float64{100, 50}, [2]float64{-1400, 300}},
		// A window on a 3x display above the main one.
		{"above 3x", [4]int{0, -1200, 800, 600}, [2]int{1200, 900}, [2]float64{300, 150}, [2]float64{200, -1100}},
	}
	for _, c := range cases {
		fc := &fakeCapturer{window: func(int, uint32) (*Capture, error) {
			return CaptureOf(solid(c.img[0], c.img[1], 0, 0, 0)), nil
		}}
		m := &WindowMapper{Capturer: fc, Scales: &WindowScales{}, Session: "capture"}
		w := WindowTarget{PID: 1, ID: 2, Bounds: c.bounds}
		// A drag maps both ends with one scale read.
		got, err := m.Map(context.Background(), w, c.px, [2]float64{0, 0})
		if err != nil {
			t.Fatal(err)
		}
		if got[0] != c.want {
			t.Errorf("%s: %v, want %v", c.name, got[0], c.want)
		}
		if got[1] != [2]float64{float64(c.bounds[0]), float64(c.bounds[1])} {
			t.Errorf("%s: the origin maps to %v", c.name, got[1])
		}
		if fc.count("window") != 1 || fc.all()[0].Session != "capture" {
			t.Errorf("%s: %+v", c.name, fc.all())
		}
	}
	// A title two windows share cannot name window pixels.
	ws := []protocol.Window{{Ref: "w1", Title: "Untitled"}, {Ref: "w2", Title: "untitled "}, {Ref: "w3", Title: "Other"}}
	var pe *protocol.Error
	if err := UniqueTitle(ws, "Untitled"); !errors.As(err, &pe) || pe.Code != protocol.ErrTargetAmbiguous {
		t.Fatalf("%v", err)
	}
	if err := UniqueTitle(ws, "Other"); err != nil {
		t.Fatal(err)
	}
}

func TestReuseProvenScale(t *testing.T) {
	fc := &fakeCapturer{window: func(int, uint32) (*Capture, error) { return CaptureOf(solid(800, 600, 0, 0, 0)), nil }}
	m := &WindowMapper{Capturer: fc, Scales: &WindowScales{}}
	w := WindowTarget{PID: 1, ID: 2, Bounds: [4]int{0, 0, 400, 300}, Layout: "main@2"}
	for range 3 {
		if _, err := m.Map(context.Background(), w, [2]float64{10, 10}); err != nil {
			t.Fatal(err)
		}
	}
	if fc.count("window") != 1 {
		t.Fatalf("%d window reads for one window at the same bounds", fc.count("window"))
	}
	moved := w
	moved.Bounds = [4]int{50, 0, 400, 300}
	_, _ = m.Map(context.Background(), moved, [2]float64{1, 1})
	relaid := w
	relaid.Layout = "main@2+left@1"
	_, _ = m.Map(context.Background(), relaid, [2]float64{1, 1})
	if fc.count("window") != 3 {
		t.Fatalf("%d reads: other bounds or another display layout must read again", fc.count("window"))
	}
	// A window shot proves the scale for later pixel actions.
	svc := NewService(&fakeCapturer{window: func(int, uint32) (*Capture, error) { return CaptureOf(solid(800, 600, 0, 0, 0)), nil }}, func(string) (uint32, bool) { return 2, true })
	win := protocol.Window{Ref: "w1", PID: 1, Bounds: bounds(0, 0, 400, 300)}
	if _, err := svc.Observe(context.Background(), "a", protocol.ObserveRequest{Image: ImageWindow}, windowBase(win)); err != nil {
		t.Fatal(err)
	}
	if k, ok := svc.Windows.Proven(WindowKey{PID: 1, ID: 2, Bounds: [4]int{0, 0, 400, 300}}); !ok || k != [2]float64{2, 2} {
		t.Fatalf("proven %v %v", k, ok)
	}
}

func TestTemplateFindsMovedCrop(t *testing.T) {
	screen := ui(400, 300)
	screen.Fill(Rect{250, 180, 30, 20}, 200, 30, 30)
	screen.Fill(Rect{258, 186, 14, 8}, 250, 250, 250)
	crop, _, _ := screen.Crop(Rect{240, 170, 50, 40})
	moved := ui(400, 300)
	moved.Fill(Rect{110, 60, 30, 20}, 200, 30, 30)
	moved.Fill(Rect{118, 66, 14, 8}, 250, 250, 250)
	m := FindTemplate(moved, crop, 1, 1)
	if m.Score < MatchBar || m.X != 125 || m.Y != 70 {
		t.Fatalf("%+v", m)
	}
	if m := FindTemplate(solid(200, 100, 9, 9, 9), crop, 1, 1); m.Score >= MatchBar {
		t.Fatalf("a flat screen matched: %+v", m)
	}
}

func TestTemplateMatchesAcrossScales(t *testing.T) {
	// A crop recorded on a 2x display matches a 1x screen.
	screen := ui(400, 300)
	screen.Fill(Rect{200, 100, 40, 20}, 20, 160, 60)
	screen.Fill(Rect{210, 105, 8, 10}, 250, 250, 250)
	part, _, _ := screen.Crop(Rect{190, 90, 60, 40})
	big := magnify(part, 2)
	m := FindTemplate(screen, big, 1, 2)
	if m.Score < MatchBar || m.X < 218 || m.X > 222 || m.Y < 108 || m.Y > 112 {
		t.Fatalf("%+v", m)
	}
}
