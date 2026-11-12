package plan

import (
	"context"
	"fmt"

	"fennec/cua/internal/protocol"
)

// imagePoint aims a background click with a button, count or modifiers at
// a desktop point p of its window in that window's own image pixels: the
// point relative to the window times the image's pixels per desktop unit,
// which a capture of the window (by its app's pid and its key, never by
// title) tells. The click then goes to that window alone, never to a window
// covering it.
func (r *planRun) imagePoint(ctx context.Context, s protocol.Step, t protocol.Target, p protocol.Point) (*protocol.Target, *stop) {
	w, st := r.targetWindow(ctx, s, t)
	if st != nil {
		return nil, st
	}
	if w.Bounds == nil || w.Bounds[2] <= 0 || w.Bounds[3] <= 0 {
		return nil, &stop{err: &protocol.Error{Code: protocol.ErrWindowNotFound, Message: fmt.Sprintf("%q has no frame, and a background click with a button, count or modifiers needs one to reach its window; nothing was sent", w.Title), Hint: "use foreground input"}}
	}
	iw, _, err := r.e.WindowImage(ctx, w.PID, w.Key)
	if err != nil || iw <= 0 {
		why := "it has no image"
		if err != nil {
			why = err.Error()
		}
		return nil, &stop{err: &protocol.Error{Code: protocol.ErrUnavailable, Message: fmt.Sprintf("%q could not be captured to aim a background click with a button, count or modifiers in its pixels (%s); nothing was sent", w.Title, why), Hint: "use foreground input"}}
	}
	k := float64(iw) / float64(w.Bounds[2])
	return &protocol.Target{
		Point:  &protocol.Point{X: (p.X - float64(w.Bounds[0])) * k, Y: (p.Y - float64(w.Bounds[1])) * k, Space: protocol.SpaceWindow},
		Window: r.windowRef(w),
	}, nil
}
