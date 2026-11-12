package vision

import (
	"context"
	"strings"
	"time"
)

// Settling after an input.
const (
	// ThumbEdge is the long edge of the thumbnails a settle compares.
	ThumbEdge = 160
	// ThumbTolerance is the channel difference that makes two thumbnails differ.
	ThumbTolerance = 48
	// SettleWait is how long a settle waits for a first change.
	SettleWait = 600 * time.Millisecond
	// ConfirmWait is the first-change wait after input that confirms
	// something (Return, or text ending in a newline), which may load a page.
	ConfirmWait = 1500 * time.Millisecond
	// SettleMax caps a settle that saw a change.
	SettleMax = 3 * time.Second
	// SettlePoll is the thumbnail interval.
	SettlePoll = 40 * time.Millisecond
	// SettleHold is how long a changed screen must hold still.
	SettleHold = 40 * time.Millisecond
	// PageHold is the hold of a settle after input that may commit a page load.
	PageHold = 400 * time.Millisecond
)

// Thumb shrinks a capture to a settle thumbnail.
func Thumb(f *Frame) *Frame { return Lanczos(f, ThumbEdge) }

// Differs says whether two thumbnails differ: in size, or by more than
// ThumbTolerance in some channel of some pixel.
func Differs(a, b *Frame) bool {
	if a.W != b.W || a.H != b.H {
		return true
	}
	for i := range a.Pix {
		d := int(a.Pix[i]) - int(b.Pix[i])
		if d > ThumbTolerance || d < -ThumbTolerance {
			return true
		}
	}
	return false
}

// SettleOptions shape one settle.
type SettleOptions struct {
	// Wait is the first-change wait (default SettleWait).
	Wait time.Duration
	// Page is input that may commit a browser page load: the settle holds
	// PageHold and, while the screen differs from before, may last SettleMax.
	Page bool
}

// SettleWaitFor is the first-change wait of an input: ConfirmWait for a
// Return or text ending in a newline, unless it is a left click in a
// dialog, which waits SettleWait like any other input.
func SettleWaitFor(action, key, text string, inDialog bool) time.Duration {
	if inDialog && strings.HasSuffix(action, "click") {
		return SettleWait
	}
	switch {
	case action == "key" && strings.EqualFold(key, "return"):
		return ConfirmWait
	case action == "type" && (strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\r")):
		return ConfirmWait
	}
	return SettleWait
}

// ThumbSource gives one probe capture per call, from a session apart from
// the action's, with its thumbnail.
type ThumbSource interface {
	Thumb(ctx context.Context) (*Frame, *Capture, error)
}

// Settled is a settle's outcome.
type Settled struct {
	// Still is set when the screen changed and then held still; Capture is
	// then the probe capture it held still on.
	Still   bool
	Changed bool
	Capture *Capture
	Elapsed time.Duration
}

// Settle waits for the screen to differ from first and then hold still. It
// lasts at least the first-change wait, extends to SettleWait past the
// first change but never beyond SettleMax, and ends once the changed
// screen held still for the hold time. A page change that reverts to the
// screen before the input ends at its first-change deadline.
func Settle(ctx context.Context, clock Clock, first *Frame, src ThumbSource, opts SettleOptions) Settled {
	wait := opts.Wait
	if wait <= 0 {
		wait = SettleWait
	}
	hold := SettleHold
	if opts.Page {
		hold = PageHold
	}
	start := clock.Now()
	base := start.Add(wait)
	deadline := base
	var (
		last           *Frame
		lastCap        *Capture
		since          = start
		still, changed bool
	)
	for !still && clock.Now().Before(deadline) {
		now := clock.Now()
		nap := min(SettlePoll, deadline.Sub(now))
		if changed {
			nap = min(nap, since.Add(hold).Sub(now))
		}
		if clock.Sleep(ctx, nap) != nil {
			break
		}
		thumb, c, err := src.Thumb(ctx)
		if err != nil {
			_ = clock.Sleep(ctx, deadline.Sub(clock.Now()))
			break
		}
		now = clock.Now()
		differs := Differs(first, thumb)
		if differs && !changed {
			if t := minTime(now.Add(SettleWait), start.Add(SettleMax)); t.After(base) {
				base = t
			}
		}
		changed = differs
		if last == nil || Differs(last, thumb) {
			since, last, lastCap = now, thumb, c
		}
		deadline = base
		if opts.Page && changed {
			deadline = start.Add(SettleMax)
		}
		still = changed && now.Sub(since) >= hold
	}
	out := Settled{Still: still, Changed: changed, Elapsed: clock.Now().Sub(start)}
	if still {
		out.Capture = lastCap
	}
	return out
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// ProbeSource takes settle thumbnails in a probe session of their own, so
// the action session's capture context is untouched.
type ProbeSource struct {
	Capturer Capturer
	Session  string
}

// Thumb takes a full-size capture and its thumbnail.
func (p ProbeSource) Thumb(ctx context.Context) (*Frame, *Capture, error) {
	c, err := p.Capturer.Desktop(ctx, p.Session, 0)
	if err != nil {
		return nil, nil, err
	}
	f, err := c.Pixels()
	if err != nil {
		return nil, nil, err
	}
	return Thumb(f), c, nil
}
