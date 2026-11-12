package vision

import (
	"context"
	"time"
)

// Stillness.
const (
	// StillEdge is the width of the captures stillness compares.
	StillEdge = 320
	// BlockSize is the side of the blocks a capture is split into.
	BlockSize = 8
	// VisibleStep is the channel difference a block change must exceed.
	VisibleStep  = 24
	StillTimeout = 600 * time.Millisecond
	StillMax     = 5 * time.Second
	StillHold    = 40 * time.Millisecond
	StillPoll    = 10 * time.Millisecond
)

// Clock is time for waits; tests use a fake one.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the wall clock.
type RealClock struct{}

// Now is time.Now.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep waits d or until ctx ends.
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Blocks is a capture split into BlockSize blocks, with a hash per block.
type Blocks struct {
	frame  *Frame
	cols   int
	hashes []uint64
}

// BlocksOf hashes a frame's blocks (FNV-1a over each block's pixels).
func BlocksOf(f *Frame) *Blocks {
	cols := (f.W + BlockSize - 1) / BlockSize
	rows := (f.H + BlockSize - 1) / BlockSize
	h := make([]uint64, cols*rows)
	for i := range h {
		h[i] = 0xcbf29ce484222325
	}
	for y := 0; y < f.H; y++ {
		row := y / BlockSize * cols
		for x := 0; x < f.W; x++ {
			b := &h[row+x/BlockSize]
			i := (y*f.W + x) * 3
			for _, c := range f.Pix[i : i+3] {
				*b = (*b ^ uint64(c)) * 0x100000001b3
			}
		}
	}
	return &Blocks{frame: f, cols: cols, hashes: h}
}

// visible says whether block i differs between a and b by more than
// VisibleStep in some channel of some pixel.
func visible(a, b *Blocks, i int) bool {
	bx, by := i%a.cols*BlockSize, i/a.cols*BlockSize
	f, g := a.frame, b.frame
	for y := by; y < min(by+BlockSize, f.H); y++ {
		for p := (y*f.W + bx) * 3; p < (y*f.W+min(bx+BlockSize, f.W))*3; p++ {
			d := int(f.Pix[p]) - int(g.Pix[p])
			if d > VisibleStep || d < -VisibleStep {
				return true
			}
		}
	}
	return false
}

// Block kinds by what a block showed during a wait.
const (
	Quiet    = "quiet"    // changed at most once
	Toggling = "toggling" // went back and forth between two contents (a caret)
	Churning = "churning" // went through three or more contents (a clock, a spinner, video)
)

// Classify names what a block's sequence of contents was, and for a
// toggling block its two contents.
func Classify(contents []uint64) (string, [2]uint64) {
	var seen []uint64
	changes := 0
	for i, c := range contents {
		if i > 0 && c == contents[i-1] {
			continue
		}
		if i > 0 {
			changes++
		}
		known := false
		for _, s := range seen {
			known = known || s == c
		}
		if !known {
			seen = append(seen, c)
		}
	}
	switch {
	case changes < 2:
		return Quiet, [2]uint64{}
	case len(seen) == 2:
		return Toggling, [2]uint64{seen[0], seen[1]}
	}
	return Churning, [2]uint64{}
}

// counts says whether a block that showed contents and now shows now has
// changed in a way that counts: any change of a quiet block, a third
// content of a toggling one, never a churning one.
func counts(contents []uint64, now uint64) bool {
	kind, pair := Classify(contents)
	switch kind {
	case Toggling:
		return now != pair[0] && now != pair[1]
	case Churning:
		return false
	}
	return true
}

// Watch follows the contents of every block during one wait.
type Watch struct {
	runs [][]uint64
}

// Counts records now after last and says whether some block changed in a
// way that counts: visibly, and not a block that churns or toggled back to
// a content it showed. A change of capture size always counts.
func (w *Watch) Counts(last, now *Blocks) bool {
	if last.frame.W != now.frame.W || last.frame.H != now.frame.H {
		w.runs = nil
		return true
	}
	if w.runs == nil {
		w.runs = make([][]uint64, len(last.hashes))
		for i, h := range last.hashes {
			w.runs[i] = []uint64{h}
		}
	}
	counted := false
	for i, was := range last.hashes {
		is := now.hashes[i]
		if was == is {
			continue
		}
		if counts(w.runs[i], is) && visible(last, now, i) {
			counted = true
		}
		w.runs[i] = append(w.runs[i], is)
	}
	return counted
}

// Volatile counts the blocks that churned or toggled during the wait.
func (w *Watch) Volatile() int {
	n := 0
	for _, r := range w.runs {
		if k, _ := Classify(r); k != Quiet {
			n++
		}
	}
	return n
}

// FrameSource gives one whole-screen capture per call.
type FrameSource interface {
	Frame(ctx context.Context) (*Frame, error)
}

// StillOptions bound a stillness wait; zero values take the defaults.
type StillOptions struct {
	Timeout time.Duration // default StillTimeout, at most StillMax
	Hold    time.Duration // default StillHold
}

// StillResult answers a stillness wait.
type StillResult struct {
	Still    bool `json:"still"`
	Changed  bool `json:"changed"`
	Ms       int  `json:"ms"`
	Volatile int  `json:"volatile"`
}

// Still waits until no block has changed in a way that counts for the hold
// time, or the timeout passes (checked between captures).
func Still(ctx context.Context, clock Clock, src FrameSource, opts StillOptions) (StillResult, error) {
	limit := opts.Timeout
	if limit <= 0 {
		limit = StillTimeout
	}
	limit = min(limit, StillMax)
	hold := opts.Hold
	if hold <= 0 {
		hold = StillHold
	}
	start := clock.Now()
	f, err := src.Frame(ctx)
	if err != nil {
		return StillResult{}, err
	}
	last := BlocksOf(f)
	var watch Watch
	since, changed := clock.Now(), false
	for {
		now := clock.Now()
		still := now.Sub(since) >= hold
		if still || now.Sub(start) >= limit {
			return StillResult{Still: still, Changed: changed, Ms: int(now.Sub(start).Milliseconds()), Volatile: watch.Volatile()}, nil
		}
		if err := clock.Sleep(ctx, StillPoll); err != nil {
			return StillResult{}, err
		}
		f, err := src.Frame(ctx)
		if err != nil {
			return StillResult{}, err
		}
		next := BlocksOf(f)
		if watch.Counts(last, next) {
			changed, since = true, clock.Now()
		}
		last = next
	}
}

// StillSource captures the whole screen StillEdge wide in a capture
// session; the captures carry no pixels back to the agent.
type StillSource struct {
	Capturer Capturer
	Session  string
}

// Frame takes one capture.
func (s StillSource) Frame(ctx context.Context) (*Frame, error) {
	c, err := s.Capturer.Desktop(ctx, s.Session, StillEdge)
	if err != nil {
		return nil, err
	}
	f, err := c.Pixels()
	if err != nil {
		return nil, err
	}
	if f.W > StillEdge && f.W >= f.H {
		f = Fit(f, StillEdge)
	}
	return f, nil
}
