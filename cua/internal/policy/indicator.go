package policy

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"
)

// Indicator states: what the menu-bar indicator tells the user about
// fennec-cua. Stopped wins over acting, and acting over idle; recording is
// a mark on top of idle or acting.
const (
	StateIdle      = "idle"
	StateActing    = "acting"
	StateStopped   = "stopped"
	StateRecording = "recording"
)

// Icon names, one per look of the indicator.
const (
	IconIdle            = "idle"             // an outlined ring
	IconActing          = "acting"           // a filled disc
	IconStopped         = "stopped"          // an outlined ring struck through
	IconRecording       = "recording"        // the ring with a dot in it
	IconActingRecording = "acting-recording" // the disc with a dot cut out of it
)

// Icons lists every icon name.
var Icons = []string{IconIdle, IconActing, IconStopped, IconRecording, IconActingRecording}

// IconPoints is the indicator's size in points: a menu-bar template image
// is 18 points square.
const IconPoints = 18

// IndicatorView is what the menu bar shows: the state, its icon (a template
// image, so it follows the menu bar's appearance), the tooltip and the
// menu's lines. The first menu line, like the tooltip, names the version
// and the input mode.
type IndicatorView struct {
	State     string
	Recording bool
	Icon      string
	Tooltip   string
	Menu      []string
}

// Indicator is the state model Fennec's UI renders in the menu bar. It
// redraws (calls Draw) only when what it shows changes.
type Indicator struct {
	Version string
	Input   string
	// Draw shows a view; nil draws nothing.
	Draw func(IndicatorView)

	mu        sync.Mutex
	acting    int
	stopped   bool
	recording bool
	drawn     *IndicatorView
}

// Acting marks input in progress until the returned end is called; calls
// may overlap.
func (in *Indicator) Acting() (end func()) {
	in.mu.Lock()
	in.acting++
	in.mu.Unlock()
	in.redraw()
	var once sync.Once
	return func() {
		once.Do(func() {
			in.mu.Lock()
			in.acting--
			in.mu.Unlock()
			in.redraw()
		})
	}
}

// Stopped shows a stop (true) or a resume.
func (in *Indicator) Stopped(stopped bool) {
	in.mu.Lock()
	in.stopped = stopped
	in.mu.Unlock()
	in.redraw()
}

// Recording shows a recording starting (true) or ending.
func (in *Indicator) Recording(on bool) {
	in.mu.Lock()
	in.recording = on
	in.mu.Unlock()
	in.redraw()
}

// View is what the indicator shows now.
func (in *Indicator) View() IndicatorView {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.view()
}

func (in *Indicator) view() IndicatorView {
	v := IndicatorView{State: StateIdle, Recording: in.recording && !in.stopped}
	switch {
	case in.stopped:
		v.State, v.Icon = StateStopped, IconStopped
	case in.acting > 0 && v.Recording:
		v.State, v.Icon = StateActing, IconActingRecording
	case in.acting > 0:
		v.State, v.Icon = StateActing, IconActing
	case v.Recording:
		v.Icon = IconRecording
	default:
		v.Icon = IconIdle
	}
	head := fmt.Sprintf("fennec-cua %s · %s input", orDev(in.Version), orForeground(in.Input))
	say := map[string]string{StateIdle: "idle", StateActing: "acting on the desktop", StateStopped: "stopped by you"}[v.State]
	if v.Recording {
		say += ", recording"
	}
	v.Tooltip = head + " · " + say
	v.Menu = []string{head, "Status: " + say}
	if in.stopped {
		v.Menu = append(v.Menu, "Resume")
	} else {
		v.Menu = append(v.Menu, "Stop")
	}
	return v
}

func orDev(v string) string {
	if v == "" {
		return "dev"
	}
	return v
}

func orForeground(m string) string {
	if m == "" {
		return "foreground"
	}
	return m
}

// redraw draws the view when it differs from the one drawn last.
func (in *Indicator) redraw() {
	in.mu.Lock()
	v := in.view()
	same := in.drawn != nil && sameView(*in.drawn, v)
	if !same {
		in.drawn = &v
	}
	draw := in.Draw
	in.mu.Unlock()
	if !same && draw != nil {
		draw(v)
	}
}

func sameView(a, b IndicatorView) bool {
	if a.State != b.State || a.Recording != b.Recording || a.Icon != b.Icon || a.Tooltip != b.Tooltip || len(a.Menu) != len(b.Menu) {
		return false
	}
	for i := range a.Menu {
		if a.Menu[i] != b.Menu[i] {
			return false
		}
	}
	return true
}

// IconPNG renders an icon as a template image: black with coverage in the
// alpha channel only, IconPoints points square at scale pixels per point
// (1 or 2 for Retina).
func IconPNG(name string, scale int) ([]byte, error) {
	if scale < 1 {
		return nil, fmt.Errorf("icon scale %d is not 1 or more", scale)
	}
	shape, ok := iconShapes[name]
	if !ok {
		return nil, fmt.Errorf("no indicator icon %q", name)
	}
	n := IconPoints * scale
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	const sub = 4 // samples per pixel side
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			hits := 0
			for sy := 0; sy < sub; sy++ {
				for sx := 0; sx < sub; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/sub) / float64(scale)
					py := (float64(y) + (float64(sy)+0.5)/sub) / float64(scale)
					if shape(px, py) {
						hits++
					}
				}
			}
			img.SetNRGBA(x, y, color.NRGBA{A: uint8(math.Round(255 * float64(hits) / (sub * sub)))})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Icon geometry in points on the 18-point square.
const (
	iconC    = 9.0  // centre
	ringOut  = 7.0  // outer radius of the ring and the disc
	ringIn   = 5.5  // inner radius of the ring
	dotR     = 2.5  // the recording dot
	strikeHW = 0.85 // half the strike's width
)

func dist(x, y float64) float64 { return math.Hypot(x-iconC, y-iconC) }

func ring(x, y float64) bool { d := dist(x, y); return d <= ringOut && d >= ringIn }

func disc(x, y float64) bool { return dist(x, y) <= ringOut }

func dot(x, y float64) bool { return dist(x, y) <= dotR }

// strike is the stroke from the lower left to the upper right.
func strike(x, y float64) bool {
	// Distance from the line x + y = 18, within the disc's reach.
	return math.Abs(x+y-2*iconC)/math.Sqrt2 <= strikeHW && dist(x, y) <= ringOut+0.5
}

var iconShapes = map[string]func(x, y float64) bool{
	IconIdle:            ring,
	IconActing:          disc,
	IconStopped:         func(x, y float64) bool { return ring(x, y) || strike(x, y) },
	IconRecording:       func(x, y float64) bool { return ring(x, y) || dot(x, y) },
	IconActingRecording: func(x, y float64) bool { return disc(x, y) && !dot(x, y) },
}
