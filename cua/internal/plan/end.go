package plan

import (
	"context"
	"strings"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// shellApps are desktop-shell processes whose windows an escalation never
// lists.
var shellApps = map[string]bool{"dock": true, "window server": true, "windowmanager": true, "systemuiserver": true, "control center": true, "controlcenter": true, "notification center": true, "notificationcenter": true, "wallpaper": true, "spotlight": true, "finder desktop": true}

// Notes on a cut failure outline.
const (
	NoteReadCut   = "the accessibility read stopped early, so the outline misses the rest of the window; scroll or open the relevant pane to bring it in"
	NoteBudgetCut = "the outline was cut at its text budget; observe with find, or observe the window whole, for the rest"
)

// failureView is the view a stopped plan answers: the open windows minus
// the desktop shell and the plan window's outline, with a note when the
// outline is cut.
func (r *planRun) failureView(ctx context.Context) *protocol.ObserveResult {
	req := protocol.ObserveRequest{}
	if r.win != nil {
		listed, err := r.list(ctx)
		if err == nil && byKey(listed, r.win.Key) != nil {
			req.Window = r.windowRef(*r.win)
		}
	}
	ob, err := r.e.Observer.Observe(ctx, req)
	if err != nil && req.Window != "" {
		ob, err = r.e.Observer.Observe(ctx, protocol.ObserveRequest{})
	}
	if err != nil || ob == nil {
		return nil
	}
	var keep []protocol.Window
	for _, w := range ob.Windows {
		if !shellApps[strings.ToLower(strings.TrimSpace(w.App))] {
			keep = append(keep, w)
		}
	}
	ob.Windows = keep
	var notes []string
	for _, n := range ob.Notes {
		switch {
		case strings.HasPrefix(n, "outline cut: the window has more than"):
			notes = append(notes, NoteReadCut)
		case strings.HasPrefix(n, "outline cut at the"):
			notes = append(notes, NoteBudgetCut)
		default:
			notes = append(notes, n)
		}
	}
	ob.Notes = notes
	return ob
}

// endWait waits about a second after a last step that may open a window
// (a shortcut, a line ending in Return or Tab, an arrow in front of a menu
// or dialog, a press, click, menu or close) and reports a window it opened
// as the current one. It answers whether the plan ended settled: no new
// window came.
func (r *planRun) endWait(ctx context.Context) bool {
	last := r.steps[len(r.steps)-1]
	kind, _ := last.Kind()
	before := r.lastBefore
	if !canOpen(last, kind, frontOf(before)) {
		return true
	}
	now, err := r.list(ctx)
	if err != nil {
		return true
	}
	opened := newWindows(before, now)
	if len(opened) == 0 {
		changedNow, ws, err := r.windowChange(ctx, now, EndWait)
		if err != nil || !changedNow {
			return true
		}
		opened = newWindows(before, ws)
		now = ws
	}
	if len(opened) == 0 {
		return true
	}
	f := frontOf(now)
	res := &r.res.Steps[len(r.res.Steps)-1]
	ch := res.Changed
	if ch == nil {
		ch = &protocol.Change{}
	}
	for _, w := range opened {
		if !contains(ch.Opened, w.Title) {
			ch.Opened = append(ch.Opened, w.Title)
		}
	}
	if f != nil {
		ch.Front = f.Title
		if byKey(opened, f.Key) != nil && (r.win == nil || f.PID == r.win.PID) {
			w := *f
			r.win = &w
		}
	}
	res.Changed = ch
	return false
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// canOpen says whether a step may open a window that shows only after it.
func canOpen(s protocol.Step, kind string, front *view.RawWindow) bool {
	switch kind {
	case "press", "click", "menu", "close":
		return true
	case "type":
		t := *s.Type
		return strings.HasSuffix(t, "\n") || strings.HasSuffix(t, "\t")
	case "key":
		keys, err := actions.KeyNames(s.Key)
		if err != nil || len(keys) == 0 {
			return false
		}
		mods, main := map[string]bool{}, keys[len(keys)-1]
		for _, k := range keys[:len(keys)-1] {
			mods[k] = true
		}
		if mods["cmd"] || mods["ctrl"] || mods["alt"] {
			return true
		}
		switch main {
		case "return", "tab", "escape", "space":
			return true
		case "up", "down", "left", "right":
			return front != nil && (dialogRole(front.Role) || strings.HasPrefix(front.Role, "menu") || front.Role == "popover")
		}
		return false
	}
	return false
}

// windowChange waits up to limit for the window list to differ from base
// (a window opened or closed, the front or a title changed) and answers
// the list that differs, or the last one read. It waits on the daemon's
// window-change wait when there is one, which reads the list only when the
// windows may have changed; otherwise it polls the list.
func (r *planRun) windowChange(ctx context.Context, base []view.RawWindow, limit time.Duration) (bool, []view.RawWindow, error) {
	if r.e.WindowChange == nil {
		ww := actions.WindowWait{List: r.e.X.B.Windows, Filter: func(ws []view.RawWindow) []view.RawWindow {
			return r.e.policy().Allow.Filter(view.ListWindows(ws, r.e.policy().Input))
		}, Now: r.e.now, Sleep: r.e.sleep}
		return ww.Wait(ctx, base, limit)
	}
	last := base
	done, err := r.e.WindowChange(ctx, limit, func(ctx context.Context) (bool, error) {
		ws, err := r.list(ctx)
		if err != nil {
			return false, err
		}
		last = ws
		return !sameWindows(base, ws), nil
	})
	return done, last, err
}

// sameWindows says whether two lists show the same windows, titles and
// front window in the same order.
func sameWindows(a, b []view.RawWindow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].Title != b[i].Title || a[i].Active != b[i].Active {
			return false
		}
	}
	return true
}
