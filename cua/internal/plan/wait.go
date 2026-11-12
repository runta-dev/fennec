package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fennec/cua/internal/engine"
	"fennec/cua/internal/models"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
	"fennec/cua/internal/vision"
)

// MaxWait caps one wait.
const MaxWait = 60 * time.Second

// outcome is a condition's verdict.
type outcome struct {
	met      bool
	evidence string
	why      string // why it is not met
	change   *protocol.Change
	win      *view.RawWindow // the window a title wait settled on
	served   []protocol.Served
	volatile int
	// confirm marks a newly opened window that proves the step's effect.
	confirm bool
}

// waiter checks conditions against the desktop.
type waiter struct {
	r       *planRun
	before  []view.RawWindow
	bg      bool
	planWin *view.RawWindow
}

// waitFor polls a wait until it holds, the timeout passes or it fails for
// good (a perr: ambiguity, a model failure, a check background mode cannot
// make).
func (w *waiter) waitFor(ctx context.Context, req protocol.WaitRequest, def time.Duration) (outcome, *protocol.Error) {
	timeout := def
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	timeout = min(timeout, MaxWait)
	if emptyWait(req) {
		_ = w.r.e.sleep(ctx, timeout)
		return outcome{met: true, evidence: fmt.Sprintf("waited %d ms", timeout.Milliseconds())}, nil
	}
	if req.Still {
		return w.still(ctx, req, timeout)
	}
	deadline := w.r.e.now().Add(timeout)
	var last outcome
	for {
		o, perr := w.check(ctx, req)
		if perr != nil {
			return o, perr
		}
		if o.met {
			return o, nil
		}
		last = o
		if !w.r.e.now().Before(deadline) {
			return last, nil
		}
		if err := w.r.e.sleep(ctx, min(Poll, deadline.Sub(w.r.e.now()))); err != nil {
			return last, engineErr(err)
		}
	}
}

// check tests every condition of a wait once.
func (w *waiter) check(ctx context.Context, req protocol.WaitRequest) (outcome, *protocol.Error) {
	listed, err := w.r.list(ctx)
	if err != nil {
		return outcome{why: err.Error()}, nil
	}
	out := outcome{met: true}
	var ev []string
	e := req.Expect
	if req.Changed {
		ch := changed(w.before, listed)
		if ch == nil {
			return outcome{why: "the window list did not change (a title change alone does not count)"}, nil
		}
		out.change = ch
		ev = append(ev, "the window list changed")
	}
	if e.WindowOpens != "" {
		win, perr := titled(newWindows(w.before, listed), e.WindowOpens, true)
		if perr != nil {
			return outcome{}, perr
		}
		if win == nil {
			return outcome{why: fmt.Sprintf("no new window %q opened", e.WindowOpens)}, nil
		}
		if w.planWin != nil && win.PID == w.planWin.PID && dialogRole(win.Role) && win.OwnerKey != "" && win.OwnerKey != w.planWin.Key {
			return outcome{why: fmt.Sprintf("the new window %q belongs to another window of the app", win.Title)}, nil
		}
		out.win = win
		out.confirm = true
		out.change = &protocol.Change{Opened: []string{win.Title}}
		ev = append(ev, fmt.Sprintf("%q opened", win.Title))
	}
	if e.WindowGone != "" {
		for _, l := range listed {
			if view.TitleMatches(2, l.Title, e.WindowGone) {
				return outcome{why: fmt.Sprintf("%q is still open", l.Title)}, nil
			}
		}
		out.change = &protocol.Change{Closed: []string{e.WindowGone}}
		ev = append(ev, fmt.Sprintf("%q is gone", e.WindowGone))
	}
	if e.Front != "" {
		o, perr := w.front(ctx, listed, e.Front)
		if perr != nil || !o.met {
			return o, perr
		}
		out.win, out.confirm = o.win, o.confirm
		if o.change != nil {
			out.change = o.change
		}
		ev = append(ev, o.evidence)
	}
	for _, c := range []struct {
		t    *protocol.Target
		gone bool
	}{{e.Exists, false}, {e.Gone, true}} {
		if c.t == nil {
			continue
		}
		n, _, perr := w.find(ctx, listed, *c.t)
		if perr != nil {
			return outcome{}, perr
		}
		if (n != nil) == c.gone {
			if c.gone {
				return outcome{why: fmt.Sprintf("%s is still there", targetText(*c.t))}, nil
			}
			return outcome{why: fmt.Sprintf("no %s", targetText(*c.t))}, nil
		}
		if c.gone {
			ev = append(ev, targetText(*c.t)+" is gone")
		} else {
			ev = append(ev, targetText(*c.t)+" is there")
		}
	}
	if e.Reads != nil {
		o, perr := w.reads(ctx, listed, *e.Reads, e.Equals)
		if perr != nil || !o.met {
			return o, perr
		}
		ev = append(ev, o.evidence)
	}
	if e.Looks != "" {
		o, perr := w.looks(ctx, e.Looks)
		out.served = append(out.served, o.served...)
		if perr != nil {
			return o, perr
		}
		if !o.met {
			// A model's no is final; asking again would only spend.
			return o, &protocol.Error{Code: protocol.ErrExpectFailed, Message: "expectation not met: " + o.why}
		}
		ev = append(ev, o.evidence)
	}
	out.evidence = strings.Join(ev, "; ")
	return out, nil
}

// changed is the change between two window lists, by window identity and
// the front window; a window that only changed its title is no change.
func changed(before, now []view.RawWindow) *protocol.Change {
	ch := &protocol.Change{}
	for _, w := range now {
		if byKey(before, w.Key) == nil {
			ch.Opened = append(ch.Opened, w.Title)
		}
	}
	for _, w := range before {
		if byKey(now, w.Key) == nil {
			ch.Closed = append(ch.Closed, w.Title)
		}
	}
	bf, nf := frontOf(before), frontOf(now)
	if nf != nil && (bf == nil || bf.Key != nf.Key) {
		ch.Front = nf.Title
	}
	if len(ch.Opened) == 0 && len(ch.Closed) == 0 && ch.Front == "" {
		return nil
	}
	return ch
}

func newWindows(before, now []view.RawWindow) []view.RawWindow {
	var out []view.RawWindow
	for _, w := range now {
		if byKey(before, w.Key) == nil {
			out = append(out, w)
		}
	}
	return out
}

// titled picks the one window of a title: one whose title equals it over
// those that only contain it; several that only contain it are ambiguous.
// An alert whose title merely contains the text is never taken.
func titled(ws []view.RawWindow, title string, strict bool) (*view.RawWindow, *protocol.Error) {
	var exact, loose []view.RawWindow
	for _, w := range ws {
		switch {
		case view.TitleMatches(0, w.Title, title) || view.TitleMatches(1, w.Title, title):
			exact = append(exact, w)
		case view.TitleMatches(2, w.Title, title) && w.Role != "alert":
			loose = append(loose, w)
		}
	}
	pool := exact
	if len(pool) == 0 {
		pool = loose
	}
	switch {
	case len(pool) == 0:
		return nil, nil
	case len(pool) == 1:
		w := pool[0]
		return &w, nil
	}
	if f := frontOf(pool); f != nil && !strict {
		return f, nil
	}
	return nil, &protocol.Error{Code: protocol.ErrTargetAmbiguous, Message: fmt.Sprintf("%d windows match %q, so none was taken; nothing was sent", len(pool), title), Hint: "windows: " + titlesOf(pool) + "; use a more exact name"}
}

// front waits for a window to come to the front by itself; it is never
// focused to pass, except that with no window in front at all the one
// listed window of that title is taken and fronted (foreground only).
// Under background input a new window of the plan's app with that title
// counts.
func (w *waiter) front(ctx context.Context, listed []view.RawWindow, title string) (outcome, *protocol.Error) {
	f := frontOf(listed)
	if w.bg && w.planWin != nil {
		var mine []view.RawWindow
		for _, l := range newWindows(w.before, listed) {
			if l.PID == w.planWin.PID && (l.OwnerKey == "" || l.OwnerKey == w.planWin.Key) && l.Role != "sheet" {
				mine = append(mine, l)
			}
		}
		got, perr := titled(mine, title, true)
		if perr != nil {
			return outcome{}, perr
		}
		if got != nil {
			return outcome{met: true, win: got, confirm: true, evidence: fmt.Sprintf("%q opened in the plan's app", got.Title), change: &protocol.Change{Opened: []string{got.Title}}}, nil
		}
		if p := byKey(listed, w.planWin.Key); p != nil && (view.TitleMatches(0, p.Title, title) || view.TitleMatches(1, p.Title, title)) {
			return outcome{met: true, win: p, evidence: fmt.Sprintf("%q is the plan's window", p.Title)}, nil
		}
		return outcome{why: fmt.Sprintf("no window %q opened in the plan's app", title)}, nil
	}
	if f != nil {
		if f.Role == "alert" && !(view.TitleMatches(0, f.Title, title) || view.TitleMatches(1, f.Title, title)) {
			return outcome{why: fmt.Sprintf("an alert %q is in front, not %q", f.Title, title)}, nil
		}
		if view.TitleMatches(0, f.Title, title) || view.TitleMatches(1, f.Title, title) || (view.TitleMatches(2, f.Title, title) && uniqueLoose(listed, title)) {
			o := outcome{met: true, win: f, evidence: fmt.Sprintf("%q is in front", f.Title)}
			if byKey(w.before, f.Key) == nil {
				o.confirm = true
				o.change = &protocol.Change{Opened: []string{f.Title}, Front: f.Title}
			} else if bf := frontOf(w.before); bf == nil || bf.Key != f.Key {
				o.change = &protocol.Change{Front: f.Title}
			}
			return o, nil
		}
		// Several windows behind the front one: never guess.
		var behind []view.RawWindow
		for _, l := range listed {
			if view.TitleMatches(2, l.Title, title) && l.Role != "alert" {
				behind = append(behind, l)
			}
		}
		if len(behind) > 1 {
			if _, perr := titled(behind, title, true); perr != nil {
				return outcome{}, perr
			}
		}
		return outcome{why: fmt.Sprintf("the front window is %q, not %q", f.Title, title)}, nil
	}
	got, perr := titled(listed, title, true)
	if perr != nil {
		return outcome{}, perr
	}
	if got == nil {
		return outcome{why: fmt.Sprintf("no window is in front and none is titled %q", title)}, nil
	}
	if w.bg {
		return outcome{met: true, win: got, evidence: fmt.Sprintf("took %q, the one window of that title", got.Title)}, nil
	}
	if perr := w.r.e.policy().Activity.Check(); perr != nil {
		return outcome{}, perr
	}
	end := w.r.acting()
	err := w.r.e.X.B.Raise(ctx, *got)
	end()
	if err != nil {
		return outcome{}, &protocol.Error{Code: protocol.ErrFocusMismatch, Message: fmt.Sprintf("%q could not be brought to the front: %v", got.Title, err)}
	}
	return outcome{met: true, win: got, evidence: fmt.Sprintf("no window was in front; fronted %q, the one window of that title", got.Title), change: &protocol.Change{Front: got.Title}}, nil
}

func uniqueLoose(listed []view.RawWindow, title string) bool {
	n := 0
	for _, l := range listed {
		if view.TitleMatches(2, l.Title, title) {
			n++
		}
	}
	return n == 1
}

func targetText(t protocol.Target) string {
	switch {
	case t.Ref != "":
		return t.Ref
	case t.Select != "":
		return t.Select
	case t.Point != nil:
		return fmt.Sprintf("the point (%g, %g)", t.Point.X, t.Point.Y)
	}
	return "the target"
}

// nonControls are roles a check matches only when its selector names the
// role.
var nonControls = map[string]bool{"document": true, "group": true, "generic": true, "scrollarea": true, "webarea": true, "application": true, "window": true, "splitgroup": true, "statictext": true, "text": true}

// find resolves a check's target in the plan window (or the front one):
// with the menu bar when the window alone does not hold it.
func (w *waiter) find(ctx context.Context, listed []view.RawWindow, t protocol.Target) (*view.RawNode, view.Snapshot, *protocol.Error) {
	var win *view.RawWindow
	switch {
	case t.Window != "":
		got, perr := w.r.windowNamed(listed, t.Window, 0)
		if perr != nil {
			return nil, view.Snapshot{}, nil
		}
		win = got
	case w.planWin != nil:
		win = byKey(listed, w.planWin.Key)
	}
	if win == nil {
		win = frontOf(listed)
	}
	if win == nil {
		return nil, view.Snapshot{}, nil
	}
	if t.Ref != "" {
		if w.r.e.X.Refs == nil {
			return nil, view.Snapshot{}, nil
		}
		e, err := w.r.e.X.Refs.Lookup(t.Ref)
		if err != nil {
			return nil, view.Snapshot{}, engineErr(err)
		}
		n, _, err := w.r.e.X.B.Read(ctx, e.Handle, false, false)
		if err != nil {
			return nil, view.Snapshot{}, nil
		}
		return &n, view.Snapshot{}, nil
	}
	sel, err := view.ParseSelector(t.Select)
	if err != nil {
		return nil, view.Snapshot{}, engineErr(err)
	}
	for _, menus := range []bool{false, true} {
		snap, err := w.r.snapshot(ctx, *win, view.TreeOptions{Menus: menus})
		if err != nil {
			return nil, view.Snapshot{}, nil
		}
		m, err := view.Find(snap, sel)
		if err != nil {
			continue
		}
		for _, i := range m.Index {
			n := snap.Tree.Nodes[i]
			if len(sel.Roles) == 0 && nonControls[n.Role] {
				continue
			}
			if menus && !strings.HasPrefix(n.Role, "menu") {
				continue
			}
			return &n, snap, nil
		}
	}
	return nil, view.Snapshot{}, nil
}

var unitAfter = regexp.MustCompile(`^\s*[^\d\s.,-][^\d]*$`)

// reads checks an element's value: a boolean against its checked state, a
// link against its URL as a read gives it, anything else against its value
// exactly (a spin button or slider may add a unit after the number).
func (w *waiter) reads(ctx context.Context, listed []view.RawWindow, t protocol.Target, equals *json.RawMessage) (outcome, *protocol.Error) {
	n, _, perr := w.find(ctx, listed, t)
	if perr != nil {
		return outcome{}, perr
	}
	if n == nil {
		return outcome{why: fmt.Sprintf("no %s to read", targetText(t))}, nil
	}
	if equals == nil {
		return outcome{met: true, evidence: targetText(t) + " is there"}, nil
	}
	var b bool
	if json.Unmarshal(*equals, &b) == nil {
		on := n.HasState("checked") || n.HasState("selected") || n.HasState("on")
		if n.HasState("mixed") {
			return outcome{why: fmt.Sprintf("%s is mixed, not %s", targetText(t), onOff(b))}, nil
		}
		if on != b {
			return outcome{why: fmt.Sprintf("%s is %s, not %s", targetText(t), onOff(on), onOff(b))}, nil
		}
		return outcome{met: true, evidence: fmt.Sprintf("%s is %s", targetText(t), onOff(b))}, nil
	}
	want := ""
	var s string
	if json.Unmarshal(*equals, &s) == nil {
		want = s
	} else {
		var num json.Number
		if err := json.Unmarshal(*equals, &num); err != nil {
			return outcome{}, &protocol.Error{Code: protocol.ErrInvalid, Message: "equals takes text, a number or true/false"}
		}
		want = num.String()
	}
	got := ""
	if n.Role == "link" {
		full, _, err := w.r.e.X.B.Read(ctx, n.Handle, true, false)
		if err == nil {
			got = full.URL
		}
		if got == "" {
			got = n.URL
		}
	} else {
		if n.Value != nil {
			got = *n.Value
		}
		if full, _, err := w.r.e.X.B.Read(ctx, n.Handle, true, false); err == nil && full.Value != nil {
			got = *full.Value
		}
	}
	if ValueEquals(n.Role, got, want) {
		return outcome{met: true, evidence: fmt.Sprintf("%s reads %q", targetText(t), want)}, nil
	}
	return outcome{why: fmt.Sprintf("%s reads %q, not %q", targetText(t), view.Clip(got, 80, false), want)}, nil
}

// ValueEquals compares a value read with the one wanted: exactly, except
// that a spin button or slider may carry a unit after the same number.
func ValueEquals(role, got, want string) bool {
	if got == want {
		return true
	}
	if role != "spinbutton" && role != "slider" && role != "incrementor" {
		return false
	}
	wf, err := strconv.ParseFloat(strings.TrimSpace(want), 64)
	if err != nil {
		return false
	}
	num := numberPrefix.FindString(strings.TrimSpace(got))
	if num == "" {
		return false
	}
	gf, err := strconv.ParseFloat(num, 64)
	if err != nil || gf != wf {
		return false
	}
	rest := strings.TrimSpace(got)[len(num):]
	return rest == "" || unitAfter.MatchString(rest)
}

var numberPrefix = regexp.MustCompile(`^-?\d+(\.\d+)?`)

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// looks asks the ask model a yes/no question about a screenshot; only a
// real boolean answers it.
func (w *waiter) looks(ctx context.Context, question string) (outcome, *protocol.Error) {
	if w.bg {
		return outcome{}, &protocol.Error{Code: protocol.ErrExpectFailed, Message: fmt.Sprintf("expectation %q cannot be checked: %s", question, noPixels), Hint: "check it with reads or exists, or use foreground input"}
	}
	if w.r.e.models() == nil || w.r.e.Screen == nil {
		return outcome{}, &protocol.Error{Code: protocol.ErrModelUnavailable, Message: "a visual expectation needs a model and screen captures, which are not configured", Hint: "check it with reads or exists instead"}
	}
	shot, err := w.r.e.Screen.Shot(ctx)
	if err != nil {
		return outcome{}, &protocol.Error{Code: protocol.ErrUnavailable, Message: "the screen capture failed: " + err.Error()}
	}
	b64, err := pngB64(shot.Frame)
	if err != nil {
		return outcome{}, engineErr(err)
	}
	prompt := "Look at this screenshot and answer the question with only a JSON object {\"answer\": true or false, \"reason\": \"why, briefly\"}. Question: " + question
	a, err := w.r.e.models().Ask(ctx, models.AskArgs{Prompt: prompt, Images: []string{b64}})
	if err != nil {
		return outcome{}, engineErr(err)
	}
	served := []protocol.Served{a.Served}
	verdict, reason, ok := parseVerdict(a.Text)
	if !ok {
		return outcome{served: served, why: fmt.Sprintf("the model's answer to %q was not true or false: %s", question, view.Clip(a.Text, 120, false))}, nil
	}
	if !verdict {
		return outcome{served: served, why: fmt.Sprintf("%q: no (%s)", question, reason)}, nil
	}
	return outcome{met: true, served: served, evidence: fmt.Sprintf("%q: yes (%s)", question, reason)}, nil
}

// parseVerdict reads {"answer": bool, "reason": text} from a reply; a
// verdict that is not a JSON boolean is no verdict.
func parseVerdict(text string) (bool, string, bool) {
	s := strings.TrimSpace(text)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &m) != nil {
		return false, "", false
	}
	raw, ok := m["answer"]
	if !ok {
		return false, "", false
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return false, "", false
	}
	var reason string
	_ = json.Unmarshal(m["reason"], &reason)
	return b, reason, true
}

// still waits for the screen to hold still.
func (w *waiter) still(ctx context.Context, req protocol.WaitRequest, timeout time.Duration) (outcome, *protocol.Error) {
	if w.bg {
		return outcome{}, &protocol.Error{Code: protocol.ErrUnavailable, Message: "a stillness wait reads the screen: " + noPixels}
	}
	if w.r.e.Screen == nil {
		return outcome{}, &protocol.Error{Code: protocol.ErrUnavailable, Message: "a stillness wait needs screen captures, which are not available"}
	}
	res, err := vision.Still(ctx, clock{w.r.e}, frames{w.r.e.Screen}, vision.StillOptions{Timeout: timeout, Hold: time.Duration(req.HoldMs) * time.Millisecond})
	if err != nil {
		return outcome{}, engineErr(err)
	}
	o := outcome{met: res.Still, volatile: res.Volatile, evidence: fmt.Sprintf("the screen held still after %d ms", res.Ms)}
	if !res.Still {
		o.why = fmt.Sprintf("the screen kept changing for %d ms", res.Ms)
	}
	return o, nil
}

type clock struct{ e *Executor }

func (c clock) Now() time.Time                                   { return c.e.now() }
func (c clock) Sleep(ctx context.Context, d time.Duration) error { return c.e.sleep(ctx, d) }

type frames struct{ s Screen }

func (f frames) Frame(ctx context.Context) (*vision.Frame, error) {
	shot, err := f.s.Shot(ctx)
	if err != nil {
		return nil, err
	}
	fr := shot.Frame
	if fr.W > vision.StillEdge && fr.W >= fr.H {
		fr = vision.Fit(fr, vision.StillEdge)
	}
	return fr, nil
}

// ---------------------------------------------------------------------------
// Wait steps, expectations and the wait tool

// waitStep waits inside a plan; the window it settles on becomes the plan
// window.
func (r *planRun) waitStep(ctx context.Context, i int, s protocol.Step, mode string) (protocol.StepResult, *stop) {
	before, _ := r.list(ctx)
	wt := &waiter{r: r, before: before, bg: r.background(mode), planWin: r.win}
	o, perr := wt.waitFor(ctx, *s.Wait, ExpectWait)
	res := protocol.StepResult{Served: o.served, Changed: o.change}
	if perr != nil {
		res.Status, res.Error = protocol.StatusRefused, perr
		return res, &stop{err: perr}
	}
	if !o.met {
		perr := &protocol.Error{Code: protocol.ErrTimeout, Message: "the wait did not hold: " + o.why, Hint: "observe the desktop; nothing was sent"}
		res.Status, res.Error = protocol.StatusRefused, perr
		return res, &stop{err: perr}
	}
	if o.win != nil {
		w := *o.win
		r.win = &w
	}
	res.OK, res.Status, res.Evidence = true, protocol.StatusConfirmed, o.evidence
	return res, nil
}

// expectAfter checks a step's expectation; a front window the recording
// saw is a recorded check.
func (r *planRun) expectAfter(ctx context.Context, i int, s protocol.Step, res protocol.StepResult) (protocol.StepResult, *stop) {
	mode, _ := r.mode(s)
	wt := &waiter{r: r, before: r.lastBefore, bg: r.background(mode), planWin: r.win}
	def := ExpectWait
	if s.Expect.Front != "" {
		def = FrontWait
	}
	o, perr := wt.waitFor(ctx, protocol.WaitRequest{Expect: *s.Expect}, def)
	res.Served = append(res.Served, o.served...)
	recorded := s.Expect.Front != ""
	if perr != nil {
		return res, &stop{err: perr, recorded: recorded && perr.Code != protocol.ErrCreditsExhausted}
	}
	if !o.met {
		msg := "expectation not met: " + o.why
		if s.Expect.Front != "" {
			actual := "no window"
			if listed, err := r.list(ctx); err == nil {
				if f := frontOf(listed); f != nil {
					actual = quote(f.Title)
				}
			}
			msg = fmt.Sprintf("the recording saw %q in front after this step, but the front window is %s", s.Expect.Front, actual)
		}
		code := protocol.ErrExpectFailed
		if r.background(mode) && s.Expect.Looks == "" && (s.Expect.Exists != nil || s.Expect.Reads != nil) {
			msg += "; " + noPixels + ", so it was not checked on screen"
		}
		return res, &stop{err: &protocol.Error{Code: code, Message: msg, Hint: "observe the window before going on"}, recorded: recorded}
	}
	if o.win != nil {
		w := *o.win
		r.win = &w
	}
	if o.confirm && res.Status == protocol.StatusUnconfirmed && res.Error == nil {
		res.Status, res.OK = protocol.StatusConfirmed, true
		res.Retry = nil
		var keep []pendingStep
		for _, p := range r.pending {
			if p.index != i {
				keep = append(keep, p)
			}
		}
		r.pending = keep
	}
	if o.change != nil && res.Changed == nil {
		res.Changed = o.change
	}
	if o.evidence != "" {
		res.Evidence = strings.TrimPrefix(res.Evidence+"; "+o.evidence, "; ")
	}
	return res, nil
}

// WaitTool answers the wait tool. It reads only, so it takes no desk turn,
// except to front the one window a title wait finds while none is in
// front.
func (e *Executor) WaitTool() engine.Tool {
	return func(ctx context.Context, call *engine.Call, args json.RawMessage) (any, *protocol.Error) {
		var req protocol.WaitRequest
		if perr := engine.Decode(args, &req); perr != nil {
			return nil, perr
		}
		if emptyWait(req) && req.TimeoutMs == 0 {
			return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "wait needs a condition: window_opens, window_gone, front, exists, gone, reads, looks, still or changed"}
		}
		start := e.now()
		r := &planRun{e: e, call: call, input: e.policy().Input, res: &protocol.ActResult{}}
		before, err := r.list(ctx)
		if err != nil {
			return nil, engineErr(err)
		}
		wt := &waiter{r: r, before: before, bg: r.background(r.input)}
		if req.Front != "" && frontOf(before) == nil && !wt.bg {
			release, err := turnOf(call)(ctx)
			if err != nil {
				return nil, engine.TurnError(err)
			}
			defer release()
		}
		o, perr := wt.waitFor(ctx, req, ExpectWait)
		if perr != nil {
			return nil, perr
		}
		out := &protocol.WaitResult{Met: o.met, Ms: int(e.now().Sub(start).Milliseconds()), Changed: o.change, Volatile: o.volatile}
		if !o.met && e.Observer != nil {
			if ob, err := e.Observer.Observe(ctx, protocol.ObserveRequest{Scope: protocol.ScopeDesktop}); err == nil {
				ob.Notes = append(ob.Notes, o.why)
				out.Observe = ob
			}
		}
		return out, nil
	}
}
