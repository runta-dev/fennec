package plan

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// keptTree is a front window's tree the vetting before a step read; the
// step's first read of that window takes it instead of reading again, as
// no input came in between.
type keptTree struct {
	key  string
	snap view.Snapshot
}

// treeOf is a window's tree for the current step: the vetting's when it
// read that window, else a fresh read.
func (r *planRun) treeOf(ctx context.Context, w view.RawWindow) (view.Snapshot, error) {
	if k := r.kept; k != nil && k.key == w.Key {
		return k.snap, nil
	}
	return r.snapshot(ctx, w, view.TreeOptions{})
}

func vetKey(w view.RawWindow) string {
	return w.Key + "\x00" + strconv.Itoa(w.PID) + "\x00" + w.Title
}

// alertWindow says whether a window is an alert, by its role or its tree's
// root.
func alertWindow(w view.RawWindow, snap view.Snapshot) bool {
	if w.Role == "alert" {
		return true
	}
	return len(snap.Tree.Nodes) > 0 && snap.Tree.Nodes[0].Role == "alert"
}

// vet reads a front window the plan has not seen before a step, once per
// window and title: a window the plan expects (listed at the start, taken
// by a launch or wait, or named by the step), a wait's own window and
// background input read nothing. An alert in front stops the plan before
// the step sends anything, as no plan types into a confirmation it did
// not plan for; a window retitled into an alert is read again. The tree
// read serves the step's first read of that window.
func (r *planRun) vet(ctx context.Context, s protocol.Step, kind, mode string) *stop {
	r.kept = nil
	if kind == "launch" || r.background(mode) {
		return nil
	}
	f := frontOf(r.lastBefore)
	if f == nil || f.Stale {
		return nil
	}
	if strings.TrimSpace(f.Title) != "" && r.known[f.Title] {
		return nil
	}
	if r.vetted[vetKey(*f)] {
		return nil
	}
	if s.Window != "" && view.TitleMatches(2, f.Title, s.Window) {
		return nil
	}
	if kind == "wait" && f.Role != "alert" && awaits(*s.Wait, f.Title) {
		return nil
	}
	snap, err := r.snapshot(ctx, *f, view.TreeOptions{})
	if err != nil {
		return nil
	}
	r.vetted[vetKey(*f)] = true
	if alertWindow(*f, snap) {
		w := *f
		r.win = &w
		return &stop{err: unplannedAlert(w, "")}
	}
	r.kept = &keptTree{key: f.Key, snap: snap}
	return nil
}

// awaits says whether a wait names a window of this title.
func awaits(w protocol.WaitRequest, title string) bool {
	for _, t := range []string{w.Front, w.WindowOpens} {
		if t != "" && view.TitleMatches(2, title, t) {
			return true
		}
	}
	return false
}

// unplannedAlert is the stop at an alert the plan did not plan for.
func unplannedAlert(w view.RawWindow, after string) *protocol.Error {
	msg := fmt.Sprintf("the alert %q is in front, and the plan did not plan for it; nothing more was sent", w.Title)
	if after != "" {
		msg = fmt.Sprintf("the alert %q came to the front %s, and the plan did not plan for it; nothing more was sent", w.Title, after)
	}
	return &protocol.Error{Code: protocol.ErrFocusMismatch, Message: msg, Hint: "read the alert, answer it with a press step on its button, then send the rest"}
}

// shortcut says whether a key step is a shortcut that may open a window
// some time later (a modifier chord or a function key).
func shortcut(key string) bool {
	keys, err := actions.KeyNames(key)
	if err != nil || len(keys) == 0 {
		return false
	}
	for _, k := range keys[:len(keys)-1] {
		if k == "cmd" || k == "ctrl" || k == "alt" {
			return true
		}
	}
	main := keys[len(keys)-1]
	if len(main) >= 2 && main[0] == 'f' {
		if _, err := strconv.Atoi(main[1:]); err == nil {
			return true
		}
	}
	return false
}

// dispatchAfter runs a step whose step before sent a shortcut while
// pressed was in front (nil when it did not).
func (r *planRun) dispatchAfter(ctx context.Context, i int, s protocol.Step, kind string, pressed *view.RawWindow) (protocol.StepResult, *stop) {
	r.late = pressed
	defer func() { r.late = nil }()
	return r.dispatch(ctx, i, s, kind)
}
