package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// launchReply is the part of cua-driver's launch_app reply the app step
// reads.
type launchReply struct {
	PID int `json:"pid"`
	// Background is set when macOS left another app in front.
	Background bool   `json:"background"`
	Title      string `json:"title"`
}

// appStep brings up an app: it takes a window the app already has (by the
// app's name first, then a window title holding the name as whole words),
// escalates on an app that stopped answering, and otherwise launches it
// once and takes the window the launch raised. A URL goes to the app's
// running instance. In the foreground the window is fronted when it is not
// in front or the launch stayed behind; the background fronts nothing.
func (r *planRun) appStep(ctx context.Context, i int, s protocol.Step, mode string) (protocol.StepResult, *stop) {
	name := strings.TrimSpace(s.Launch)
	pol := r.e.policy()
	if perr := pol.Allow.Launch(name, s.Args); perr != nil {
		return refused(perr), &stop{err: perr}
	}
	listed, err := r.list(ctx)
	if err != nil {
		perr := engineErr(err)
		return refused(perr), &stop{err: perr}
	}
	bg := r.background(mode)
	owned := ownedBy(listed, name)
	if len(owned) == 0 {
		for _, w := range listed {
			if view.TitleHasWords(w.Title, name) {
				owned = append(owned, w)
			}
		}
	}
	var stale []view.RawWindow
	for _, w := range owned {
		if w.Stale {
			stale = append(stale, w)
		}
	}
	if s.URL != "" {
		for _, w := range listed {
			if w.Stale && w.App != "" && suffixWords(name, w.App) && !containsKey(stale, w.Key) {
				stale = append(stale, w)
			}
		}
	}
	if len(stale) > 0 {
		perr := &protocol.Error{
			Code:    protocol.ErrNotResponding,
			Message: fmt.Sprintf("%s stopped answering: its window %q (pid %d) does not respond to accessibility, so it was not launched again", orStr(stale[0].App, name), stale[0].Title, stale[0].PID),
			Hint:    "affected windows: " + titlesOf(stale) + "; wait for the app or quit it, then run the plan again",
		}
		return refused(perr), &stop{err: perr}
	}
	if s.URL != "" || len(s.Args) > 0 {
		return r.openIn(ctx, i, s, mode, name, listed)
	}
	if len(owned) > 0 {
		w := pickOwned(owned, bg)
		r.win = &w
		res := okResult(fmt.Sprintf("took %q of %s, which was already open", w.Title, orStr(w.App, name)))
		if !bg && !w.Active {
			if st := r.front(ctx, w, &res); st != nil {
				return res, st
			}
		}
		res.Changed = &protocol.Change{Front: w.Title}
		return res, nil
	}
	before := map[string]bool{}
	pids := map[int]bool{}
	for _, w := range listed {
		before[w.Key] = true
		pids[w.PID] = true
	}
	end := r.acting()
	raw, err := r.e.X.B.Driver(ctx, "launch_app", map[string]any{"name": name})
	end()
	if err != nil {
		perr := &protocol.Error{Code: protocol.ErrUnavailable, Message: fmt.Sprintf("launching %q failed: %v", name, err), Hint: "use the app's name as Finder shows it"}
		if strings.Contains(strings.ToLower(err.Error()), "not found") || strings.Contains(strings.ToLower(err.Error()), "no app") {
			perr.Code = protocol.ErrTargetNotFound
		}
		return refused(perr), &stop{err: perr}
	}
	var rep launchReply
	_ = json.Unmarshal(raw, &rep)
	var got *view.RawWindow
	deadline := r.e.now().Add(LaunchWait)
	for {
		ws, err := r.list(ctx)
		if err == nil {
			got = launched(ws, before, pids, rep, name)
		}
		if got != nil || !r.e.now().Before(deadline) {
			break
		}
		if err := r.e.sleep(ctx, Poll/2); err != nil {
			break
		}
	}
	if got == nil {
		perr := &protocol.Error{Code: protocol.ErrWindowNotFound, Message: fmt.Sprintf("%s was launched but showed no window within %s", name, LaunchWait), Hint: "observe scope desktop; the app may be asking something or opening slowly"}
		res := protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: "asked macOS to open " + name, Error: perr}
		return res, &stop{err: perr}
	}
	r.win = got
	res := okResult(fmt.Sprintf("launched %s and took its window %q", name, got.Title))
	if !bg && (rep.Background || !got.Active) {
		if st := r.front(ctx, *got, &res); st != nil {
			return res, st
		}
	}
	res.Changed = &protocol.Change{Opened: []string{got.Title}, Front: got.Title}
	return res, nil
}

// launched is the window a launch raised: the window the reply names, else
// the first new window of the launched process, else of a process that was
// not running before. A window of another running app is never taken,
// whatever its title became.
func launched(ws []view.RawWindow, before map[string]bool, pids map[int]bool, rep launchReply, name string) *view.RawWindow {
	for _, w := range ws {
		if before[w.Key] || w.Stale {
			continue
		}
		if rep.PID != 0 && w.PID != rep.PID {
			continue
		}
		if rep.PID == 0 && pids[w.PID] && !view.AppMatches(name, w.App) {
			continue
		}
		if rep.Title != "" && w.Title == rep.Title {
			nw := w
			return &nw
		}
	}
	for _, w := range ws {
		if before[w.Key] || w.Stale {
			continue
		}
		if rep.PID != 0 && w.PID != rep.PID {
			continue
		}
		if rep.PID == 0 && pids[w.PID] && !view.AppMatches(name, w.App) {
			continue
		}
		nw := w
		return &nw
	}
	return nil
}

// front brings the plan window to the front once, after a launch that
// stayed behind or for a window that is not in front.
func (r *planRun) front(ctx context.Context, w view.RawWindow, res *protocol.StepResult) *stop {
	if perr := r.e.policy().Activity.Check(); perr != nil {
		return &stop{err: perr}
	}
	end := r.acting()
	err := r.e.X.B.Raise(ctx, w)
	end()
	if err != nil {
		perr := &protocol.Error{Code: protocol.ErrFocusMismatch, Message: fmt.Sprintf("%q could not be brought to the front: %v", w.Title, err)}
		return &stop{err: perr}
	}
	r.e.policy().Activity.Own()
	res.Notes = append(res.Notes, "fronted by raising the window")
	return nil
}

// openIn hands a URL or documents to the app and takes the window that
// shows it.
func (r *planRun) openIn(ctx context.Context, i int, s protocol.Step, mode, name string, listed []view.RawWindow) (protocol.StepResult, *stop) {
	before := map[string]bool{}
	for _, w := range listed {
		before[w.Key] = true
	}
	q := r.request(mode)
	q.Window = ""
	res := r.send(ctx, i, protocol.Step{Launch: s.Launch, URL: s.URL, Args: s.Args}, q)
	if !res.OK {
		perr := res.Error
		if perr == nil {
			perr = &protocol.Error{Code: protocol.ErrUnavailable, Message: "the launch failed"}
		}
		return res, &stop{err: perr}
	}
	deadline := r.e.now().Add(LaunchWait)
	var got *view.RawWindow
	for {
		ws, err := r.list(ctx)
		if err == nil {
			mine := ownedBy(ws, name)
			for _, w := range mine {
				if !before[w.Key] {
					nw := w
					got = &nw
					break
				}
			}
			if got == nil {
				for _, w := range mine {
					if w.Active {
						nw := w
						got = &nw
						break
					}
				}
			}
			if got == nil && len(mine) > 0 && r.e.now().Add(LaunchWait/2).After(deadline) {
				nw := mine[0]
				got = &nw
			}
		}
		if got != nil || !r.e.now().Before(deadline) {
			break
		}
		if err := r.e.sleep(ctx, Poll); err != nil {
			break
		}
	}
	if got == nil {
		perr := &protocol.Error{Code: protocol.ErrWindowNotFound, Message: fmt.Sprintf("%s showed no window for %s", name, s.URL)}
		return res, &stop{err: perr}
	}
	r.win = got
	res.Evidence = fmt.Sprintf("opened %s in %s and took its window %q", orStr(s.URL, strings.Join(s.Args, " ")), name, got.Title)
	res.Changed = &protocol.Change{Front: got.Title}
	return res, nil
}

// ownedBy are the windows of the app name names, by the app's own name.
func ownedBy(ws []view.RawWindow, name string) []view.RawWindow {
	var out []view.RawWindow
	for _, w := range ws {
		if w.App != "" && view.AppMatches(name, w.App) {
			out = append(out, w)
		}
	}
	return out
}

// pickOwned takes the active window of the app, else (in the background)
// its first titled window, else its first.
func pickOwned(ws []view.RawWindow, bg bool) view.RawWindow {
	for _, w := range ws {
		if w.Active {
			return w
		}
	}
	if bg {
		for _, w := range ws {
			if strings.TrimSpace(w.Title) != "" {
				return w
			}
		}
	}
	return ws[0]
}

// suffixWords says whether the words of app end the words of name (Chrome
// for Google Chrome); a prefix (Safari for Safari Technology Preview) does
// not count.
func suffixWords(name, app string) bool {
	n := strings.Fields(strings.ToLower(name))
	a := strings.Fields(strings.ToLower(app))
	if len(a) == 0 || len(a) > len(n) {
		return false
	}
	for i := range a {
		if n[len(n)-len(a)+i] != a[i] {
			return false
		}
	}
	return true
}

func containsKey(ws []view.RawWindow, key string) bool {
	for _, w := range ws {
		if w.Key == key {
			return true
		}
	}
	return false
}

func titlesOf(ws []view.RawWindow) string {
	var parts []string
	for _, w := range ws {
		parts = append(parts, fmt.Sprintf("%q (pid %d)", w.Title, w.PID))
	}
	return strings.Join(parts, ", ")
}
