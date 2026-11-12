package record

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/view"
)

// fakeDesk is a desktop with TextEdit (pid 7) in front and a 1Password
// vault (pid 9) behind it.
type fakeDesk struct {
	mu       sync.Mutex
	windows  []view.RawWindow
	hit      *Hit
	shots    []view.RawWindow
	holds    []int
	released int
	bundles  map[int]string
	// gate, when set, holds every window listing until it is closed.
	gate chan struct{}
}

func newDesk(hit *Hit) *fakeDesk {
	return &fakeDesk{
		hit: hit,
		windows: []view.RawWindow{
			{Key: "7/1", Title: "Untitled", Role: "window", PID: 7, Active: true, App: "TextEdit", Bounds: &[4]int{10, 20, 300, 200}},
			{Key: "9/1", Title: "Vault", Role: "window", PID: 9, App: "1Password", Bounds: &[4]int{0, 0, 100, 100}},
		},
	}
}

func (d *fakeDesk) setFront(title string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.windows[0].Title = title
}

func (d *fakeDesk) Windows(context.Context) ([]view.RawWindow, error) {
	d.mu.Lock()
	gate := d.gate
	d.mu.Unlock()
	if gate != nil {
		<-gate
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]view.RawWindow(nil), d.windows...), nil
}

func (d *fakeDesk) At(context.Context, int, int) (*Hit, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hit, nil
}

func (d *fakeDesk) Shot(_ context.Context, w view.RawWindow) ([]byte, error) {
	d.mu.Lock()
	d.shots = append(d.shots, w)
	d.mu.Unlock()
	return gray(w.Bounds[2], w.Bounds[3]), nil
}

func (d *fakeDesk) HoldWeb(_ context.Context, pid int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.holds = append(d.holds, pid)
	return nil
}

func (d *fakeDesk) ReleaseWeb(context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.released++
}

func (d *fakeDesk) Bundle(pid int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.bundles[pid]
}

func gray(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.Gray{Y: 128})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func pngWidth(t *testing.T, b64 string) int {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width
}

// record runs a session over inputs, saves it as a recording file and
// answers what the file holds.
func record(t *testing.T, d Desk, apps []string, inputs ...Input) *Recording {
	t.Helper()
	q := newQueue()
	for _, in := range inputs {
		q.push(in)
	}
	q.close()
	s := &session{id: "r", apps: apps, perPoint: 1, settle: 20 * time.Millisecond, self: 4242, desk: d, inputs: q}
	rec := s.work(context.Background())
	path := filepath.Join(t.TempDir(), "rec", "r.json")
	if err := Save(path, rec); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

// running runs a session in the background; feed sends inputs, end closes
// the input and answers the recording.
func running(d Desk, apps []string, settle time.Duration) (feed func(Input), end func() *Recording) {
	q := newQueue()
	s := &session{id: "r", apps: apps, perPoint: 1, settle: settle, self: 4242, desk: d, inputs: q}
	out := make(chan *Recording, 1)
	go func() { out <- s.work(context.Background()) }()
	time.Sleep(20 * time.Millisecond) // let the session read the desktop it starts on
	return func(in Input) { q.push(in) }, func() *Recording { q.close(); return <-out }
}

func click(x, y float64) Input { return Input{Kind: InputClick, X: x, Y: y, Count: 1} }

func key(text string, secret bool) Input {
	return Input{Kind: InputKey, Text: text, Secret: secret}
}

func kinds(r *Recording) []string {
	var out []string
	for _, e := range r.Events {
		out = append(out, e.Kind)
	}
	return out
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}
