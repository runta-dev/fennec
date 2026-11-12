package plan

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image/png"
	"math"
	"strings"

	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
	"fennec/cua/internal/vision"
)

// SizeSlack is how far, in desktop units, a window may differ from its
// recorded size for the recorded point to count.
const SizeSlack = 2

// recordedClick replays a recorded click whose target no name found: the
// recorded crop on a fresh screenshot (only when no parameter changed the
// target), then grounding, then the recorded point in a window of the
// recorded size. When all miss it stops with what each fallback found.
func (r *planRun) recordedClick(ctx context.Context, i int, s protocol.Step, kind, mode string, w view.RawWindow, sel view.Selector) (resolved, *stop) {
	rec := s.Recorded
	unchanged := sel.Name == rec.Text
	var out resolved
	var found []string
	pixels := !r.background(mode) || mode == protocol.InputAuto
	if unchanged && rec.Crop != "" {
		switch {
		case r.e.Screen == nil:
			found = append(found, "crop: no screen capture is available")
		case !pixels:
			found = append(found, "crop: "+noPixels)
		default:
			x, y, score, err := r.cropMatch(ctx, rec)
			switch {
			case err != nil:
				found = append(found, "crop: "+err.Error())
			case score < vision.MatchBar:
				found = append(found, fmt.Sprintf("crop: best match scored %.2f, under %.2f", score, vision.MatchBar))
			default:
				h := r.hitAt(ctx, int(math.Round(x)), int(math.Round(y)), w, rec.Root)
				if h.code == "" {
					if st := r.pixelsFront(ctx, mode, &out); st != nil {
						return out, st
					}
					out.target = &protocol.Target{Point: &protocol.Point{X: x, Y: y}}
					out.how = "crop"
					return out, nil
				}
				found = append(found, fmt.Sprintf("crop: matched at %.2f, but %s", score, h.why))
			}
		}
	}
	if r.e.models() != nil && r.e.Screen != nil && pixels {
		text := sel.Name
		if text == "" {
			text = rec.Text
		}
		g, st := r.groundTarget(ctx, s, mode, w, text, true)
		out.served = append(out.served, g.served...)
		out.notes = append(out.notes, g.notes...)
		if st == nil {
			g.served, g.notes = out.served, out.notes
			return g, nil
		}
		if st.err != nil && st.err.Code == protocol.ErrCreditsExhausted {
			return out, st
		}
		found = append(found, "grounding: "+st.err.Message)
		out.fg = out.fg || g.fg
	}
	if unchanged && rec.At != nil && rec.Size != nil && w.Bounds != nil {
		cw, ch := float64(w.Bounds[2]), float64(w.Bounds[3])
		if math.Abs(cw-rec.Size[0]) <= SizeSlack && math.Abs(ch-rec.Size[1]) <= SizeSlack {
			x, y := float64(w.Bounds[0])+rec.At[0], float64(w.Bounds[1])+rec.At[1]
			h := r.hitAt(ctx, int(math.Round(x)), int(math.Round(y)), w, rec.Root)
			if h.code == "" {
				out.target = &protocol.Target{Point: &protocol.Point{X: x, Y: y}}
				out.how = "point"
				return out, nil
			}
			found = append(found, "recorded point: "+h.why)
		} else {
			found = append(found, fmt.Sprintf("recorded point: the window is %gx%g, recorded at %gx%g", cw, ch, rec.Size[0], rec.Size[1]))
		}
	}
	return out, &stop{recorded: true, err: &protocol.Error{
		Code:    protocol.ErrTargetNotFound,
		Message: fmt.Sprintf("no fallback found the recorded click target %q in %q; nothing was clicked: %s", orStr(sel.Name, rec.Text), w.Title, strings.Join(found, "; ")),
		Hint:    "observe the window and finish from the remaining steps",
	}}
}

// pixelsFront takes the foreground for an auto plan before a pixel click.
func (r *planRun) pixelsFront(ctx context.Context, mode string, out *resolved) *stop {
	if mode == protocol.InputAuto && !r.fg {
		if st := r.takeForeground(ctx); st != nil {
			return st
		}
		out.fg = true
	}
	return nil
}

// cropMatch searches a fresh screenshot for a recorded crop and answers the
// desktop point of the match plus the recorded offset.
func (r *planRun) cropMatch(ctx context.Context, rec *protocol.Recorded) (float64, float64, float64, error) {
	data, err := base64.StdEncoding.DecodeString(rec.Crop)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("the recorded crop does not decode")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("the recorded crop does not decode")
	}
	tmpl := vision.FrameOf(img)
	shot, err := r.e.Screen.Shot(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("the screen capture failed: %v", err)
	}
	k := shot.Scale
	if !vision.Positive(k) {
		k = 1
	}
	scale := rec.Scale
	if !vision.Positive(scale) {
		scale = 1
	}
	m := vision.FindTemplate(shot.Frame, tmpl, 1/k, scale)
	x, y := shot.Origin[0]+m.X*k, shot.Origin[1]+m.Y*k
	if rec.Offset != nil {
		x += rec.Offset[0]
		y += rec.Offset[1]
	}
	return x, y, m.Score, nil
}
