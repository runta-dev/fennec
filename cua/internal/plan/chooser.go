package plan

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// File choosers.
const (
	// ReadyWait bounds the wait for a chooser's default button to enable
	// once the file name is typed.
	ReadyWait = 400 * time.Millisecond
	readyPoll = 20 * time.Millisecond
	// PanelWait is how long a line typed after a shortcut waits for the
	// window the shortcut may open late.
	PanelWait = time.Second
	// SubmitWait is how long a chooser may take to close after a Return.
	SubmitWait = 600 * time.Millisecond
	// submits caps the Returns one submit sends: the first may only close
	// a completion list.
	submits = 3
)

// chooserFields are the names of a save or open panel's file name field
// (or its label), without a trailing colon.
var chooserFields = map[string]bool{"save as": true, "file name": true, "filename": true}

// isChooser says whether a window is a save or open panel: a dialog (by
// its role or its tree's root) with a file name field anywhere in its tree,
// however far into it. An ordinary window with such a field is no chooser.
func isChooser(w view.RawWindow, snap view.Snapshot) bool {
	nodes := snap.Tree.Nodes
	root := ""
	if len(nodes) > 0 {
		root = nodes[0].Role
	}
	dialog := func(role string) bool { return dialogRole(role) && role != "alert" }
	if !dialog(w.Role) && !dialog(root) && !hasSheet(nodes) {
		// A save panel shown as a sheet sits in its document window's
		// tree, which is what a window in front reads.
		return false
	}
	for _, n := range nodes {
		label := n.Name
		if label == "" && n.Value != nil && (n.Role == "text" || n.Role == "statictext") {
			// A static text label carries its words as its value: the
			// save panel's "Save As:" has no name.
			label = *n.Value
		}
		name := strings.TrimSpace(strings.TrimSuffix(view.Norm(view.StripObjects(label)), ":"))
		switch n.Role {
		case "textbox", "combobox", "searchbox", "statictext", "text":
			if chooserFields[name] {
				return true
			}
		}
	}
	return false
}

// typingTarget is the window a type or key step goes to, for chooser
// checks: after a shortcut that may open a panel late, the window that
// comes to the front (waited for up to PanelWait, through moments with no
// window in front); otherwise the step's window, or a dialog of its app in
// front of it. A window that came late becomes the plan window. Background
// input looks at no front window: it takes the step's own window.
func (r *planRun) typingTarget(ctx context.Context, s protocol.Step, mode string) *view.RawWindow {
	fg := !r.background(mode)
	if p := r.late; p != nil && fg && !ownWindow(s) {
		deadline := r.e.now().Add(PanelWait)
		for {
			listed, err := r.list(ctx)
			if f := frontOf(listed); err == nil && f != nil && (f.Key != p.Key || f.Title != p.Title) {
				if r.win == nil || r.win.PID == f.PID {
					w := *f
					r.win = &w
				}
				return f
			}
			if !r.e.now().Before(deadline) {
				break
			}
			if r.e.sleep(ctx, Poll) != nil {
				return nil
			}
		}
	}
	w := r.typingWindow(ctx, s)
	if w != nil && fg && !dialogRole(w.Role) && !ownWindow(s) {
		if listed, err := r.list(ctx); err == nil {
			if f := frontOf(listed); f != nil && f.PID == w.PID && f.Key != w.Key && dialogRole(f.Role) && f.Role != "alert" {
				return f
			}
		}
	}
	return w
}

// chooserType types a line ending in a newline into a save or open panel:
// it selects the file name field's text, types the line without its
// newline, waits for the default button and submits the panel. done is
// false when the step's window is no chooser.
func (r *planRun) chooserType(ctx context.Context, i int, s protocol.Step, mode string) (protocol.StepResult, *stop, bool) {
	w := r.typingTarget(ctx, s, mode)
	if w == nil {
		return protocol.StepResult{}, nil, false
	}
	snap, err := r.treeOf(ctx, *w)
	if err != nil || !isChooser(*w, snap) {
		// The kept read may predate a panel that opened late as a sheet
		// of the same window: read it once more.
		if snap, err = r.snapshot(ctx, *w, view.TreeOptions{}); err != nil || !isChooser(*w, snap) {
			return protocol.StepResult{}, nil, false
		}
	}
	q := r.panelRequest(*w, mode)
	sel := r.send(ctx, i, protocol.Step{Key: "cmd+a", Input: s.Input}, q)
	if sel.Status == protocol.StatusRefused || (sel.Error != nil && !sel.OK) {
		return sel, &stop{err: sel.Error}, true
	}
	text := strings.TrimSuffix(strings.TrimSuffix(*s.Type, "\n"), "\r")
	typed := r.send(ctx, i, protocol.Step{Type: &text, Into: s.Into, AnyFocus: s.AnyFocus, Input: s.Input}, q)
	if typed.Status == protocol.StatusRefused || (typed.Error != nil && !typed.OK) {
		return typed, &stop{err: typed.Error}, true
	}
	res, st := r.submit(ctx, i, s, *w, q)
	res.Evidence = strings.TrimPrefix(fmt.Sprintf("typed %d characters into the file name field of %q; %s", len([]rune(text)), w.Title, res.Evidence), "; ")
	return res, st, true
}

// chooserReturn sends a Return step to a save or open panel as its submit:
// it waits for the default button first. done is false for any other
// window.
func (r *planRun) chooserReturn(ctx context.Context, i int, s protocol.Step, mode string) (protocol.StepResult, *stop, bool) {
	keys, err := actions.KeyNames(s.Key)
	if err != nil || len(keys) != 1 || (keys[0] != "return" && keys[0] != "enter") {
		return protocol.StepResult{}, nil, false
	}
	w := r.typingTarget(ctx, s, mode)
	if w == nil || !dialogRole(w.Role) {
		return protocol.StepResult{}, nil, false
	}
	snap, err := r.treeOf(ctx, *w)
	if err != nil || !isChooser(*w, snap) {
		return protocol.StepResult{}, nil, false
	}
	res, st := r.submit(ctx, i, s, *w, r.panelRequest(*w, mode))
	return res, st, true
}

// panelRequest addresses a panel's sends to the panel itself.
func (r *planRun) panelRequest(w view.RawWindow, mode string) actions.Request {
	q := r.request(mode)
	q.Window = r.windowRef(w)
	return q
}

// ready waits up to ReadyWait for a panel's default button (its last
// button) to enable.
func (r *planRun) ready(ctx context.Context, w view.RawWindow) {
	deadline := r.e.now().Add(ReadyWait)
	for {
		snap, err := r.snapshot(ctx, w, view.TreeOptions{})
		if err != nil {
			return
		}
		last := -1
		for k, n := range snap.Tree.Nodes {
			if n.Role == "button" {
				last = k
			}
		}
		if last < 0 || !snap.Tree.Nodes[last].HasState("disabled") || !r.e.now().Before(deadline) {
			return
		}
		if r.e.sleep(ctx, readyPoll) != nil {
			return
		}
	}
}

// submit presses Return in a panel once its default button is ready, and
// again (up to three Returns) only while the panel is still in front: the
// first may only close a completion list. A window that comes in front of
// the panel, such as an overwrite confirmation, gets no Return: the plan
// stops there with it as the current window.
func (r *planRun) submit(ctx context.Context, i int, s protocol.Step, w view.RawWindow, q actions.Request) (protocol.StepResult, *stop) {
	r.ready(ctx, w)
	ret := protocol.Step{Key: "return", Input: s.Input}
	sent := 0
	listed, _ := r.list(ctx)
	for k := 0; k < submits; k++ {
		if byKey(listed, w.Key) == nil {
			break
		}
		if f := frontOf(listed); k > 0 && (f == nil || f.Key != w.Key) {
			break
		}
		got := r.send(ctx, i, ret, q)
		if got.Status == protocol.StatusRefused || (got.Error != nil && !got.OK) {
			return got, &stop{err: got.Error}
		}
		sent++
		listed = r.until(ctx, SubmitWait, func(ws []view.RawWindow) bool {
			f := frontOf(ws)
			return byKey(ws, w.Key) == nil || (f != nil && f.Key != w.Key)
		})
	}
	f := frontOf(listed)
	if byKey(listed, w.Key) == nil {
		if f != nil && f.Role == "alert" && byKey(r.lastBefore, f.Key) == nil {
			a := *f
			r.win = &a
			perr := unplannedAlert(a, fmt.Sprintf("after %q closed", w.Title))
			return protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: "submitted " + quote(w.Title), Error: perr}, &stop{err: perr}
		}
		if r.win != nil && r.win.Key == w.Key {
			r.win = nil
			if f != nil && f.PID == w.PID {
				nw := *f
				r.win = &nw
			}
		}
		return okResult(fmt.Sprintf("%q closed after %d Return(s)", w.Title, sent)), nil
	}
	if f != nil && f.Key != w.Key {
		a := *f
		r.win = &a
		perr := unplannedAlert(a, fmt.Sprintf("over %q after its Return", w.Title))
		if a.Role != "alert" {
			perr.Message = strings.Replace(perr.Message, "the alert", "the window", 1)
		}
		return protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: fmt.Sprintf("sent %d Return(s) to %q", sent, w.Title), Error: perr}, &stop{err: perr}
	}
	perr := &protocol.Error{Code: protocol.ErrNotShown, Message: fmt.Sprintf("%q is still open after %d Return(s)", w.Title, sent), Hint: "observe the panel: its button may be disabled or the name refused"}
	return protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: fmt.Sprintf("sent %d Return(s)", sent), Error: perr}, &stop{err: perr}
}

// until reads the window list until ok holds or limit passes, waiting on
// window changes in between, and answers the last list.
func (r *planRun) until(ctx context.Context, limit time.Duration, ok func([]view.RawWindow) bool) []view.RawWindow {
	deadline := r.e.now().Add(limit)
	ws, err := r.list(ctx)
	if err != nil {
		return nil
	}
	for !ok(ws) {
		left := deadline.Sub(r.e.now())
		if left <= 0 {
			break
		}
		_, next, err := r.windowChange(ctx, ws, left)
		if err != nil {
			break
		}
		ws = next
	}
	return ws
}

// hasSheet says whether a window's tree holds a sheet.
func hasSheet(nodes []view.RawNode) bool {
	for _, n := range nodes {
		if n.Role == "sheet" {
			return true
		}
	}
	return false
}
