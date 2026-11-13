package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/models"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// noPixels is why a background plan never looks at the screen.
const noPixels = "background input does not read the screen, which shows whatever covers the plan's window"

// step runs one step and says whether the plan stops there.
func (r *planRun) step(ctx context.Context, i int) (protocol.StepResult, *stop) {
	s := r.steps[i]
	kind, _ := s.Kind()
	start := r.e.now()
	r.lastBefore, _ = r.list(ctx)
	pressed := r.pressed
	r.pressed = nil
	var res protocol.StepResult
	var st *stop
	if mode, perr := r.mode(s); perr == nil {
		st = r.vet(ctx, s, kind, mode)
	}
	if st != nil {
		res = refused(st.err)
	} else {
		res, st = r.dispatchAfter(ctx, i, s, kind, pressed)
	}
	if st == nil && (kind == "launch" || kind == "wait") && r.win != nil {
		r.known[r.win.Title] = true
	}
	if st == nil && kind == "key" && shortcut(s.Key) {
		r.pressed = frontOf(r.lastBefore)
	}
	if st == nil && s.Expect != nil && kind != "wait" {
		res, st = r.expectAfter(ctx, i, s, res)
	}
	if st == nil {
		r.readBack(i, s, kind, res)
	}
	if res.Action == "" {
		res.Action = kind
	}
	res.Ms = int(r.e.now().Sub(start).Milliseconds())
	if st != nil {
		res.OK = false
		if res.Error == nil {
			res.Error = st.err
		}
		if res.Status == "" || res.Status == protocol.StatusConfirmed {
			res.Status = protocol.StatusRefused
		}
	}
	return res, st
}

func (r *planRun) dispatch(ctx context.Context, i int, s protocol.Step, kind string) (protocol.StepResult, *stop) {
	mode, perr := r.mode(s)
	if perr != nil {
		return refused(perr), &stop{err: perr}
	}
	if kind != "launch" {
		if st := r.health(ctx, i, s, kind); st != nil {
			return refused(st.err), st
		}
	}
	switch kind {
	case "launch":
		return r.appStep(ctx, i, s, mode)
	case "wait":
		return r.waitStep(ctx, i, s, mode)
	case "expect":
		return protocol.StepResult{OK: true, Status: protocol.StatusConfirmed}, nil
	}
	return r.act(ctx, i, s, kind, mode)
}

// targetOf is the step's element target.
func targetOf(s protocol.Step) *protocol.Target {
	for _, t := range []*protocol.Target{s.Press, s.Set, s.Click, s.Scroll, s.Drag, s.Perform, s.SelectText} {
		if t != nil {
			return t
		}
	}
	return nil
}

// ownWindow says whether the step names its own window.
func ownWindow(s protocol.Step) bool {
	if t := targetOf(s); t != nil && t.Window != "" {
		return true
	}
	return s.Window != "" || s.App != "" || s.PID != 0
}

// request is what a step inherits: the plan window by ref, else the
// request's window.
func (r *planRun) request(mode string) actions.Request {
	q := actions.Request{Window: r.req.Window}
	if r.win != nil {
		q.Window = r.windowRef(*r.win)
	}
	if mode != protocol.InputAuto {
		q.Input = mode
	}
	return q
}

// send runs one step through the actions executor as the plan's own input.
func (r *planRun) send(ctx context.Context, i int, s protocol.Step, q actions.Request) protocol.StepResult {
	end := r.acting()
	defer end()
	return r.e.X.Step(ctx, q, s, i)
}

func (r *planRun) acting() func() {
	if r.e.Acting == nil {
		return func() {}
	}
	return r.e.Acting.Begin()
}

// act runs an input step.
func (r *planRun) act(ctx context.Context, i int, s protocol.Step, kind, mode string) (protocol.StepResult, *stop) {
	bg := r.background(mode)
	if bg && r.doubt != "" && needsWindow(kind) && !ownWindow(s) {
		perr := &protocol.Error{Code: protocol.ErrTargetAmbiguous, Message: r.doubt + "; nothing was sent", Hint: "name the window with a wait step, or use foreground input"}
		return refused(perr), &stop{err: perr}
	}
	if bg && r.win == nil && r.gone != "" && !ownWindow(s) && needsWindow(kind) {
		perr := &protocol.Error{Code: protocol.ErrWindowNotFound, Message: fmt.Sprintf("the plan's window %q is gone, and background input never takes another window of that title for it; nothing was sent", r.gone), Hint: "name the window with a wait step"}
		return refused(perr), &stop{err: perr}
	}
	// Under auto input a menu step needs no plan window: it takes the
	// foreground and runs in the front app's menu bar.
	if bg && r.win == nil && !ownWindow(s) && r.req.Window == "" && needsWindow(kind) && !(kind == "menu" && mode == protocol.InputAuto) {
		perr := &protocol.Error{Code: protocol.ErrWindowNotFound, Message: "a background plan has no target window for this " + kind + "; nothing was sent", Hint: "start the plan with a launch step, or name the window with a wait step"}
		return refused(perr), &stop{err: perr}
	}
	if !bg && r.win == nil && !ownWindow(s) && r.req.Window == "" && (kind == "key" || kind == "type") && s.Into == nil {
		if st := r.awaitFront(ctx); st != nil {
			return refused(st.err), st
		}
	}
	if s.Web != nil {
		if st := r.webCheck(ctx, i, s); st != nil {
			return refused(st.err), st
		}
	}
	if kind == "menu" && bg {
		if mode != protocol.InputAuto {
			msg := fmt.Sprintf("the macOS menu bar belongs to the front app, so background input cannot run %q; nothing was sent", s.Menu)
			if s.Value != nil {
				// A toggle's mark shows only in its open menu, on screen.
				msg = fmt.Sprintf("the state of %q shows only in its open menu, which needs a look at the screen: %s; no menu was opened", s.Menu, noPixels)
			}
			perr := &protocol.Error{Code: protocol.ErrUnavailable, Message: msg, Hint: "use the foreground or auto input mode, or a key shortcut"}
			return refused(perr), &stop{err: perr}
		}
		if st := r.takeForeground(ctx); st != nil {
			return refused(st.err), st
		}
		mode, bg = protocol.InputForeground, false
	}
	var served []protocol.Served
	how := ""
	var notes []string
	if t := targetOf(s); t != nil && (kind == "press" || kind == "click" || kind == "set") {
		got, st := r.resolve(ctx, i, s, kind, mode)
		served, notes = got.served, got.notes
		if st != nil {
			res := refused(st.err)
			res.Served, res.Notes = served, notes
			return res, st
		}
		if got.target != nil {
			s = withTarget(s, kind, *got.target)
		}
		how = got.how
		if got.grounded {
			r.unsaved = true
		}
		if got.fg {
			mode, bg = protocol.InputForeground, false
		}
		if got.clickCentre && kind == "press" {
			// A recorded right, double or modified click clicks the
			// element's centre with them instead of pressing it.
			s.Click, s.Press = s.Press, nil
			kind = "click"
		}
		if got.clickCentre && bg && r.e.WindowImage != nil {
			at, st := r.imagePoint(ctx, s, *t, *s.Click.Point)
			if st != nil {
				res := refused(st.err)
				res.Served, res.Notes = served, notes
				return res, st
			}
			s.Click = at
		}
	}
	if kind == "set" && isBool(s.Value) {
		res, st := r.toggle(ctx, i, s, mode)
		res.How = orStr(res.How, how)
		res.Served = append(served, res.Served...)
		return res, st
	}
	if kind == "set" && !isBool(s.Value) {
		if res, st, done := r.setText(ctx, i, s, mode); done {
			res.How = orStr(res.How, how)
			res.Served = append(served, res.Served...)
			return res, st
		}
	}
	if kind == "menu" {
		if res, st, done := r.menuToggle(ctx, i, s, mode); done {
			if res.Input == "" && r.e.policy().Input != protocol.InputForeground && !r.background(mode) {
				// A menu step of a background or auto plan ran in front.
				res.Input = protocol.InputForeground
			}
			return res, st
		}
	}
	if kind == "type" {
		if st := r.blankCheck(ctx, s); st != nil {
			return refused(st.err), st
		}
		if w := r.typingWindow(ctx, s); w != nil && terminalApp(w.App) {
			return r.terminalType(ctx, i, s, mode, *w)
		}
		if strings.HasSuffix(*s.Type, "\n") && !s.Secret {
			if res, st, done := r.chooserType(ctx, i, s, mode); done {
				return res, st
			}
		}
		if mode == protocol.InputAuto && !r.fg {
			return r.autoType(ctx, i, s)
		}
	}
	if kind == "key" {
		if res, st, done := r.chooserReturn(ctx, i, s, mode); done {
			return res, st
		}
	}
	before, _ := r.list(ctx)
	q := r.request(mode)
	if kind == "type" && !s.Secret {
		q.WholeText = true
	}
	// Background typing is read back from the window's fields.
	var tw *view.RawWindow
	var was windowTexts
	if kind == "type" && bg && !s.Secret {
		if tw = r.typingWindow(ctx, s); tw != nil {
			was = r.texts(ctx, *tw)
		}
	}
	res := r.send(ctx, i, s, q)
	if tw != nil && res.Status == protocol.StatusUnconfirmed && res.Error == nil && res.Input != protocol.InputForeground {
		readBackTyped(&res, was, r.texts(ctx, *tw), s, tw.Title)
	}
	res.Served = append(served, res.Served...)
	res.Notes = append(notes, res.Notes...)
	if how != "" && (res.How == "" || res.How == "click" || res.How == "press" || res.How == "hit") {
		res.How = how
	}
	if res.Input == protocol.InputForeground && mode == protocol.InputAuto {
		r.fg = true
	}
	return r.judge(ctx, i, s, kind, mode, res, before)
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func isBool(v *json.RawMessage) bool {
	if v == nil {
		return false
	}
	var b bool
	return json.Unmarshal(*v, &b) == nil
}

// needsWindow says whether a step kind sends input to a window.
func needsWindow(kind string) bool {
	switch kind {
	case "press", "set", "click", "type", "key", "scroll", "drag", "menu", "perform", "select_text":
		return true
	}
	return false
}

func withTarget(s protocol.Step, kind string, t protocol.Target) protocol.Step {
	switch kind {
	case "press":
		s.Press = &t
	case "set":
		s.Set = &t
	case "click":
		s.Click = &t
	}
	return s
}

// judge turns an actions result into the plan's verdict.
func (r *planRun) judge(ctx context.Context, i int, s protocol.Step, kind, mode string, res protocol.StepResult, before []view.RawWindow) (protocol.StepResult, *stop) {
	if res.Status == protocol.StatusRefused || (res.Error != nil && !res.OK) {
		perr := res.Error
		if perr == nil {
			perr = &protocol.Error{Code: protocol.ErrInternal, Message: "the step failed"}
		}
		recorded := kind == "type" && s.Into != nil && perr.Code == protocol.ErrFocusMismatch
		return res, &stop{err: perr, recorded: recorded}
	}
	after, _ := r.list(ctx)
	if frontOf(after) == nil || (mode == protocol.InputAuto && res.Input == protocol.InputForeground) {
		// A settle with no front window, or a step that fell back to the
		// foreground, hands no list on: the next step lists again.
		r.e.dropLists()
	}
	if r.background(mode) && res.Input != protocol.InputForeground {
		if st := r.frontSteal(before, after); st != nil {
			res.OK = false
			return res, st
		}
	}
	r.follow(kind, s, before, after)
	if res.Status == protocol.StatusUnconfirmed {
		note := strings.Join(res.Notes, "; ")
		switch kind {
		case "type":
			if hasNote(res.Notes, "terminal: true") || s.AnyFocus {
				break
			}
			perr := &protocol.Error{Code: protocol.ErrNotShown, Message: "the typed text did not show: " + orStr(note, "the field could not be read back"), Hint: "read the field before typing again; do not resend unchecked"}
			return res, &stop{err: perr}
		case "set", "close", "focus":
			code := protocol.ErrExpectFailed
			if kind == "close" {
				code = protocol.ErrNotShown
			}
			perr := &protocol.Error{Code: code, Message: fmt.Sprintf("%s did not take: %s", kind, orStr(note, res.Evidence)), Hint: "observe the window before trying again"}
			if kind == "close" && strings.Contains(note, "still open") {
				perr.Message = "the dialog is still open after the close request: " + note
			}
			return res, &stop{err: perr}
		case "menu":
			if s.Value != nil {
				perr := &protocol.Error{Code: protocol.ErrExpectFailed, Message: "the menu item is still in its old state: " + note}
				return res, &stop{err: perr}
			}
			fallthrough
		default:
			r.pending = append(r.pending, pendingStep{index: i, note: orStr(note, "sent, but its effect was not read back"), background: r.background(mode)})
		}
	}
	if res.Status == protocol.StatusConfirmed && axProof(res) {
		r.axOnly = append(r.axOnly, i)
	}
	return res, nil
}

func hasNote(notes []string, n string) bool {
	for _, x := range notes {
		if x == n {
			return true
		}
	}
	return false
}

// axProof says whether a step's only proof is an accessibility action's
// reply.
func axProof(res protocol.StepResult) bool {
	return strings.Contains(res.Evidence, "by its accessibility action")
}

// follow keeps the plan window: a dialog the plan window owns becomes the
// target, a closed plan window is remembered as gone.
func (r *planRun) follow(kind string, s protocol.Step, before, after []view.RawWindow) {
	if r.win == nil {
		if kind == "type" || kind == "key" || kind == "click" || kind == "press" {
			return
		}
		return
	}
	if w := byKey(after, r.win.Key); w == nil || w.PID != r.win.PID {
		for _, w := range after {
			if w.OwnerKey == r.win.Key {
				return
			}
		}
		r.gone = r.win.Title
		r.win = nil
		if f := frontOf(after); f != nil && !r.background(r.input) {
			w := *f
			r.win = &w
		}
		return
	}
	cur := byKey(after, r.win.Key)
	r.win = cur
	var fresh []view.RawWindow
	for _, w := range after {
		if byKey(before, w.Key) == nil && w.PID == r.win.PID && w.Key != r.win.Key {
			fresh = append(fresh, w)
		}
	}
	if r.background(r.input) {
		unowned := 0
		for _, w := range fresh {
			if w.OwnerKey == "" && dialogRole(w.Role) && w.Role != "sheet" {
				unowned++
			}
		}
		if unowned > 1 {
			r.doubt = fmt.Sprintf("%d dialogs of %s opened with no owner the system names, and background input never guesses which belongs to the plan's window", unowned, orStr(r.win.App, "the app"))
			return
		}
	}
	for _, w := range fresh {
		if w.OwnerKey == r.win.Key && (dialogRole(w.Role) || w.Title != "") {
			if w.Role == "sheet" {
				// Input for a sheet goes to the window it hangs on.
				return
			}
			if w.Role == "window" && w.Title == "" {
				continue
			}
			nw := w
			r.win = &nw
			return
		}
	}
	if !r.background(r.input) {
		if f := frontOf(after); f != nil && f.PID == r.win.PID && byKey(before, f.Key) == nil && (f.OwnerKey == "" || f.OwnerKey == r.win.Key) {
			nw := *f
			r.win = &nw
		}
	}
}

// frontSteal fails a background step whose app came to the front by
// itself.
func (r *planRun) frontSteal(before, after []view.RawWindow) *stop {
	if r.win == nil {
		return nil
	}
	was, now := frontOf(before), frontOf(after)
	if now == nil || now.PID != r.win.PID {
		return nil
	}
	if was != nil && (was.PID == r.win.PID && was.Key == now.Key) {
		return nil
	}
	if was == nil {
		return nil
	}
	return &stop{err: &protocol.Error{
		Code:    protocol.ErrFocusMismatch,
		Message: fmt.Sprintf("%q of %s came to the front by itself during a background step, though the plan sent no fronting call; later steps did not run", now.Title, orStr(now.App, "the app")),
		Hint:    "this app cannot be kept behind: use foreground input for it",
	}}
}

// takeForeground fronts the plan window for an auto plan that needs the
// foreground; the rest of the plan then runs in front.
func (r *planRun) takeForeground(ctx context.Context) *stop {
	if r.win == nil {
		r.fg = true
		return nil
	}
	end := r.acting()
	defer end()
	res := r.e.X.Step(ctx, actions.Request{Input: protocol.InputForeground}, protocol.Step{Focus: r.windowRef(*r.win)}, -1)
	if !res.OK {
		perr := res.Error
		if perr == nil {
			perr = &protocol.Error{Code: protocol.ErrFocusMismatch, Message: "the plan window could not be brought to the front; nothing was sent"}
		}
		return &stop{err: perr}
	}
	r.fg = true
	return nil
}

// readBack confirms earlier steps that this one reads back: a passing
// expect that reads values, a verify or a set.
func (r *planRun) readBack(i int, s protocol.Step, kind string, res protocol.StepResult) {
	if !res.OK {
		return
	}
	if kind == "type" && res.Status == protocol.StatusConfirmed {
		// Text read back in the field proves the click before it that
		// put the focus there.
		var keep []pendingStep
		for _, p := range r.pending {
			if p.index < i && p.index < len(r.steps) && r.steps[p.index].Click != nil {
				continue
			}
			keep = append(keep, p)
		}
		r.pending = keep
	}
	reads := kind == "set" || kind == "menu" && s.Value != nil
	if e := s.Expect; e != nil {
		reads = reads || e.Reads != nil || e.Looks != "" || e.Gone != nil || e.WindowOpens != "" || e.WindowGone != ""
	}
	if !reads {
		return
	}
	var keep []pendingStep
	for _, p := range r.pending {
		if p.index > i || (p.index == i && s.Expect == nil) {
			keep = append(keep, p)
		}
	}
	r.pending = keep
	var ax []int
	for _, a := range r.axOnly {
		if a > i || (a == i && s.Expect == nil) {
			ax = append(ax, a)
		}
	}
	r.axOnly = ax
}

// openNotes fails a plan that leaves background input sent but not
// confirmed: from behind, nothing else shows whether it landed. A
// foreground step the driver could not confirm stays unconfirmed (its
// retry false) and is named in the caveats, as the screen it acted on is
// the user's to see.
func (r *planRun) openNotes() *stop {
	p, ok := r.firstBackground()
	if !ok {
		return nil
	}
	return &stop{err: &protocol.Error{
		Code:    protocol.ErrNotShown,
		Message: fmt.Sprintf("step %d was sent but not confirmed, and no later step read it back: %s", p.index, p.note),
		Hint:    "observe to check its effect before sending it again",
	}}
}

func (r *planRun) firstBackground() (pendingStep, bool) {
	for _, p := range r.pending {
		if p.background {
			return p, true
		}
	}
	return pendingStep{}, false
}

func (r *planRun) firstOpen() int {
	if p, ok := r.firstBackground(); ok {
		return p.index
	}
	return -1
}

// caveats are steps proven only by an accessibility action's reply, and
// foreground steps sent without proof that no later step read back.
func (r *planRun) caveats() []protocol.Caveat {
	if !r.res.OK {
		return nil
	}
	var out []protocol.Caveat
	for _, i := range r.axOnly {
		out = append(out, protocol.Caveat{Step: i, Note: "proven only by the accessibility action's reply; no later step read its effect back"})
	}
	for _, p := range r.pending {
		if !p.background {
			out = append(out, protocol.Caveat{Step: p.index, Note: "sent but not confirmed: " + p.note + "; check its effect before sending it again"})
		}
	}
	return out
}

// creditsStop stops a plan on a model failure that must not fall back.
func creditsStop(err error) *stop {
	var me *models.Error
	if asModelErr(err, &me) && me.Code == protocol.ErrCreditsExhausted {
		return &stop{err: me.Protocol()}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Health before a step

// health checks the front app and the plan window before a step: a front
// app that stopped answering is waited for, a plan window that answers no
// accessibility or hides a modal dialog gets no input.
func (r *planRun) health(ctx context.Context, i int, s protocol.Step, kind string) *stop {
	listed, err := r.list(ctx)
	if err != nil {
		return nil
	}
	if f := frontOf(listed); f != nil && f.Stale && i > 0 && !ownWindow(s) {
		if st := r.hung(ctx, i, *f); st != nil {
			return st
		}
		listed, _ = r.list(ctx)
	}
	if r.win != nil && r.background(r.input) {
		if w := byKey(listed, r.win.Key); w == nil || w.PID != r.win.PID {
			r.gone, r.win = r.win.Title, nil
		} else {
			for _, l := range listed {
				if l.Key != w.Key && l.PID == w.PID && l.Title == w.Title && needsWindow(kind) {
					return &stop{err: &protocol.Error{Code: protocol.ErrTargetAmbiguous, Message: fmt.Sprintf("another window of %s is also titled %q, and background input never guesses which one gets it; nothing was sent", orStr(w.App, "the app"), w.Title), Hint: "close or rename one of them, or use foreground input"}}
				}
			}
		}
	}
	if r.win != nil {
		if w := byKey(listed, r.win.Key); w != nil {
			r.win = w
			if w.Stale && kind != "close" {
				return &stop{err: &protocol.Error{Code: protocol.ErrNotResponding, Message: fmt.Sprintf("%q (pid %d) answers no accessibility calls, so nothing was sent to it", w.Title, w.PID), Hint: "wait for the app or quit it"}}
			}
			if w.Stale && kind == "close" {
				return &stop{err: &protocol.Error{Code: protocol.ErrNotResponding, Message: fmt.Sprintf("%q (pid %d) answers no accessibility calls, so it was not closed", w.Title, w.PID), Hint: "wait for the app or quit it"}}
			}
		}
		if d := hiddenModal(listed, *r.win); d != nil && !(kind == "close" && s.Close != "" && view.TitleMatches(2, d.Title, s.Close)) {
			return &stop{err: &protocol.Error{
				Code:    protocol.ErrFocusMismatch,
				Message: fmt.Sprintf("%s holds a modal dialog %q that the screen does not show, so %q is blocked; nothing was typed or sent", orStr(r.win.App, "the app"), d.Title, r.win.Title),
				Hint:    fmt.Sprintf("close it with a close step on %q, or show it (it is a hidden modal)", d.Title),
			}}
		}
	} else if f := frontOf(listed); f != nil && f.Stale && kind != "wait" && kind != "expect" {
		return &stop{err: &protocol.Error{Code: protocol.ErrNotResponding, Message: fmt.Sprintf("the front window %q (pid %d) answers no accessibility calls; nothing was sent", f.Title, f.PID), Hint: "wait for the app or quit it"}}
	}
	return nil
}

// hiddenModal is a modal dialog of the window's app that is open but not
// shown.
func hiddenModal(listed []view.RawWindow, w view.RawWindow) *view.RawWindow {
	for _, l := range listed {
		if l.PID != w.PID || l.Key == w.Key || !dialogRole(l.Role) || l.Role == "panel" {
			continue
		}
		if l.OwnerKey != "" && l.OwnerKey != w.Key {
			continue
		}
		if !view.Shown(l) || (l.Bounds != nil && (l.Bounds[2] <= 0 || l.Bounds[3] <= 0)) {
			d := l
			return &d
		}
	}
	return nil
}

// hung waits for a front app that stopped answering after step i-1.
func (r *planRun) hung(ctx context.Context, i int, f view.RawWindow) *stop {
	deadline := r.e.now().Add(HungWait)
	for r.e.now().Before(deadline) {
		if err := r.e.sleep(ctx, Poll); err != nil {
			break
		}
		listed, err := r.list(ctx)
		if err != nil {
			continue
		}
		now := frontOf(listed)
		if now == nil {
			continue
		}
		if now.Key == f.Key && !now.Stale {
			return nil
		}
		if now.Key != f.Key && byKey(listed, f.Key) == nil && !now.Stale {
			return nil
		}
	}
	return &stop{err: &protocol.Error{
		Code:    protocol.ErrNotResponding,
		Message: fmt.Sprintf("%q (pid %d) stopped answering after step %d and did not answer again within %s; no further input was sent", f.Title, f.PID, i-1, HungWait),
		Hint:    fmt.Sprintf("once the app answers, resume from step %d", i),
	}}
}

// typingWindow is the window a type step goes to.
func (r *planRun) typingWindow(ctx context.Context, s protocol.Step) *view.RawWindow {
	listed, err := r.list(ctx)
	if err != nil {
		return nil
	}
	switch {
	case s.Window != "":
		if w, err := view.PickWindow(listed, s.Window, s.PID); err == nil {
			return &w
		}
		return nil
	case s.App != "":
		if w, err := view.PickApp(listed, s.App); err == nil {
			return &w
		}
		return nil
	case r.win != nil:
		return byKey(listed, r.win.Key)
	}
	return frontOf(listed)
}

// Terminals whose screen read-back decides typing.
var terminals = []string{"terminal", "iterm2", "iterm", "ghostty", "wezterm", "kitty", "alacritty", "warp", "hyper", "zed"}

func terminalApp(app string) bool {
	a := strings.ToLower(strings.TrimSpace(app))
	for _, t := range terminals {
		if a == t {
			return true
		}
	}
	return false
}

// awaitFront waits briefly for a front window, as when the key before is
// still opening one; with none the step stops: a plan never takes the last
// window of an earlier call.
func (r *planRun) awaitFront(ctx context.Context) *stop {
	deadline := r.e.now().Add(LateWait)
	for {
		listed, err := r.list(ctx)
		if err == nil && frontOf(listed) != nil {
			return nil
		}
		if !r.e.now().Before(deadline) {
			return &stop{err: noFront(listed)}
		}
		if err := r.e.sleep(ctx, Poll); err != nil {
			return &stop{err: engineErr(err)}
		}
	}
}
