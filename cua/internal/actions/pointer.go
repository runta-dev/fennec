package actions

import (
	"context"
	"fmt"
	"strings"

	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// textRoles take a hit-checked click: a field the keyboard focus must reach.
var textRoles = map[string]bool{"textbox": true, "textarea": true, "combobox": true, "searchbox": true}

// Hit-checked click refusals that come before anything was sent.
var hitRefusals = map[string]bool{"hit_mismatch": true, "foreground_unavailable": true, "hit_check_unavailable": true}

// isLeft says whether a button is the left (primary) one.
func isLeft(button string) bool {
	b := strings.ToLower(strings.TrimSpace(button))
	return b == "" || b == "left" || b == "primary"
}

func button(s protocol.Step) string {
	if isLeft(s.Button) {
		return "left"
	}
	return strings.ToLower(strings.TrimSpace(s.Button))
}

func count(s protocol.Step) int {
	switch {
	case s.Count <= 0:
		return 1
	case s.Count > 3:
		return 3
	}
	return s.Count
}

// spaceClick waits so that a click near the last one is not read as its
// double click; markClick records a click that may have been sent.
func (x *Executor) spaceClick(ctx context.Context, px, py int) error {
	x.mu.Lock()
	last := x.lastClick
	x.mu.Unlock()
	if !last.set {
		return nil
	}
	near := float64(SpacingPoints) * x.scale()
	dx, dy := float64(px-last.x), float64(py-last.y)
	if dx*dx+dy*dy > near*near {
		return nil
	}
	if wait := ClickSpacing - x.now().Sub(last.at); wait > 0 {
		return x.sleep(ctx, wait)
	}
	return nil
}

func (x *Executor) markClick(px, py int) {
	x.mu.Lock()
	x.lastClick = clickMark{x: px, y: py, at: x.now(), set: true}
	x.mu.Unlock()
}

func (x *Executor) scale() float64 {
	if s := x.B.Scale(); s > 0 {
		return s
	}
	return 1
}

// sendClick is one driver click; it keeps the click spacing.
func (c *stepCtx) sendClick(ctx context.Context, px, py int, args map[string]any) call {
	if err := c.x.spaceClick(ctx, px, py); err != nil {
		return call{err: err, sent: false, msg: err.Error()}
	}
	cl := driverCall(ctx, c.x.B, "click", args)
	if cl.sent || cl.err == nil {
		c.x.markClick(px, py)
	}
	if cl.sent {
		c.own()
	}
	return cl
}

func (c *stepCtx) pointerArgs(args map[string]any, btn string, n int) map[string]any {
	args["button"] = btn
	if n > 1 {
		args["count"] = n
	}
	if len(c.s.Mods) > 0 {
		var mods []string
		for _, m := range c.s.Mods {
			if k, err := keyName(m, strings.Join(c.s.Mods, "+")); err == nil {
				mods = append(mods, k)
			}
		}
		args["modifier"] = mods
	}
	return args
}

// clickWindow clicks a desktop point inside window w in that window's own
// pixels: in the foreground after raising it, falling back to a plain
// desktop click when the driver has no WindowServer connection or the
// window refuses pointer input.
func (c *stepCtx) clickWindow(ctx context.Context, w view.RawWindow, px, py int, btn string, n int, note *string) policy.Attempt {
	if c.fg() {
		if perr := c.raise(ctx, w); perr != nil {
			return refuse(perr, "")
		}
	}
	args := c.pointerArgs(c.windowArgs(w, map[string]any{}), btn, n)
	if w.Bounds != nil {
		args["x"], args["y"] = px-w.Bounds[0], py-w.Bounds[1]
	}
	cl := c.sendClick(ctx, px, py, args)
	if cl.err != nil && !cl.sent && c.fg() && (strings.Contains(cl.msg, "WindowServer") || policy.PointerRefused(cl.msg) || strings.Contains(cl.msg, "refuses pointer")) {
		return c.clickDesktop(ctx, px, py, btn, n)
	}
	if cl.err != nil {
		return failed(cl, "the click")
	}
	where := fmt.Sprintf("in %q", w.Title)
	return c.pointerResult(cl, w, fmt.Sprintf("clicked at (%d, %d) %s", px, py, where), note)
}

// clickDesktop is a plain click on the desktop at a screen point.
func (c *stepCtx) clickDesktop(ctx context.Context, px, py int, btn string, n int) policy.Attempt {
	args := c.pointerArgs(c.desktopArgs(map[string]any{"x": px, "y": py}), btn, n)
	args["delivery_mode"] = protocol.InputForeground
	cl := c.sendClick(ctx, px, py, args)
	if cl.err != nil {
		return failed(cl, "the click")
	}
	return confirmedOr(cl, fmt.Sprintf("clicked at (%d, %d) on the desktop", px, py))
}

// confirmedOr is confirmed when the driver confirmed or did not judge;
// unconfirmed with its advice otherwise.
func confirmedOr(cl call, evidence string) policy.Attempt {
	switch cl.reply.Effect {
	case "", "confirmed":
		return confirmed(evidence)
	}
	return unconfirmed(evidence, nil, nonEmpty(cl.reply.Advice())...)
}

func nonEmpty(s ...string) []string {
	var out []string
	for _, x := range s {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// pointerResult is a delivered pointer step: background input is
// unconfirmed unless the driver confirmed it; a window nobody sees gets the
// do-not-resend hint instead of the driver's advice.
func (c *stepCtx) pointerResult(cl call, w view.RawWindow, evidence string, note *string) policy.Attempt {
	var a policy.Attempt
	switch {
	case unseen(&w) && cl.reply.Effect != "confirmed":
		a = unconfirmed(evidence, nil, unseenInput)
	case !c.fg() && cl.reply.Effect != "confirmed":
		a = unconfirmed(evidence, nil, nonEmpty(cl.reply.Advice())...)
	default:
		a = confirmedOr(cl, evidence)
	}
	if note != nil && *note != "" {
		a.Result.Status = protocol.StatusUnconfirmed
		a.Result.Notes = append(a.Result.Notes, *note)
	}
	if f := cl.reply.Focused; f != nil {
		mark := ""
		if f.AtPoint {
			mark = " (the clicked field)"
		}
		a.Result.Notes = append(a.Result.Notes, fmt.Sprintf("focus: %s %q%s", f.Role, f.Name, mark))
	}
	return a
}

// click is a pointer click on an element or at a point.
func (c *stepCtx) click(ctx context.Context) policy.Attempt {
	t := *c.s.Click
	if t.Point != nil {
		return c.clickPoint(ctx, t)
	}
	e, perr := c.element(ctx, t)
	if perr != nil {
		return refuse(perr, "")
	}
	if a, ok := c.security(e); !ok {
		return a
	}
	if !c.fg() {
		return c.backgroundElementClick(ctx, e)
	}
	return c.elementClick(ctx, e)
}

// elementClick is a foreground click on an element. A plain left click on a
// text field with a window id is one hit-checked click; a refusal before
// sending falls back to the field's press action, never to a pixel click.
// Other elements get their press action; a left click the app does not
// support as an action is retried once as a hit-checked click.
func (c *stepCtx) elementClick(ctx context.Context, e element) policy.Attempt {
	listed, _ := c.windows(ctx)
	_, hasID := windowID(e.win, listed)
	left, n := isLeft(c.s.Button), count(c.s)
	if left && n == 1 && len(c.s.Mods) == 0 && textRoles[e.node.Role] && hasID {
		if perr := c.x.policy().Activity.Check(); perr != nil {
			return refuse(perr, "")
		}
		cl := classify(c.x.B.HitClick(ctx, e.win, e.node, 1))
		if cl.sent || cl.err == nil {
			c.x.markCenter(e.node.Bounds)
		}
		if cl.err == nil {
			c.own()
			return withHow(confirmedOr(cl, "clicked "+e.describe()+" after a hit test found it on top"), "hit")
		}
		if cl.sent {
			return failed(cl, "the hit-checked click")
		}
		// Refused before sending: the field's own action instead.
		err := c.x.B.Perform(ctx, e.h, "AXPress")
		if err != nil {
			return refuse(&protocol.Error{
				Code:    protocol.ErrHitMismatch,
				Message: fmt.Sprintf("the hit-checked click on %s was refused (%s) and its press action failed too (%v); nothing was clicked", e.describe(), cl.msg, err),
				Hint:    "bring the window to the front or observe again",
			}, "")
		}
		return withHow(unconfirmed("pressed "+e.describe()+" by its accessibility action", nil,
			"the hit-checked click was refused ("+cl.msg+"), so the field was pressed instead; the keyboard focus may not have moved"), "press")
	}
	action := "AXPress"
	if strings.EqualFold(c.s.Button, "right") {
		action = "AXShowMenu"
	}
	err := c.x.B.Perform(ctx, e.h, action)
	if err == nil {
		return withHow(confirmed("pressed "+e.describe()+" by its accessibility action"), "press")
	}
	if axCode(err) != AXActionUnsupported || !left || !hasID {
		return axFailure(e, err, "the press action")
	}
	if perr := c.x.policy().Activity.Check(); perr != nil {
		return refuse(perr, "")
	}
	cl := classify(c.x.B.HitClick(ctx, e.win, e.node, n))
	if cl.sent || cl.err == nil {
		c.x.markCenter(e.node.Bounds)
	}
	if cl.err == nil {
		c.own()
		return withHow(confirmedOr(cl, "clicked "+e.describe()+" after a hit test found it on top; its press action is not supported"), "hit")
	}
	if cl.sent {
		return failed(cl, "the hit-checked click")
	}
	return refuse(&protocol.Error{
		Code:    protocol.ErrHitMismatch,
		Message: fmt.Sprintf("%s does not support its press action (%v) and the hit-checked click was refused (%s); nothing was clicked", e.describe(), err, cl.msg),
		Hint:    "bring the window to the front or observe again",
	}, "")
}

func withHow(a policy.Attempt, how string) policy.Attempt {
	a.Result.How = how
	return a
}

// backgroundElementClick clicks an element's centre in its own window in
// the background. The window must hold the centre; twins the frames cannot
// tell apart are refused.
func (c *stepCtx) backgroundElementClick(ctx context.Context, e element) policy.Attempt {
	x, y, ok := center(e.node.Bounds)
	if !ok {
		return refuse(&protocol.Error{Code: protocol.ErrTargetNotFound, Message: e.describe() + " shows no point to click; nothing was sent", Hint: "press it instead"}, policy.ReasonPolicy)
	}
	listed, _ := c.windows(ctx)
	if e.win.Bounds != nil && !inside(e.win.Bounds, x, y) {
		return refuse(&protocol.Error{Code: protocol.ErrTargetAmbiguous, Message: fmt.Sprintf("the frame of window %q does not hold %s; nothing was sent", e.win.Title, e.describe()), Hint: "observe again"}, policy.ReasonPolicy)
	}
	for _, l := range listed {
		if l.Key != e.win.Key && l.PID == e.win.PID && l.Title == e.win.Title && l.Bounds != nil && e.win.Bounds != nil && *l.Bounds == *e.win.Bounds {
			return refuse(&protocol.Error{Code: protocol.ErrTargetAmbiguous, Message: fmt.Sprintf("two windows titled %q have the same frame, so the element's window cannot be told; nothing was sent", e.win.Title), Hint: "close or move one of them"}, policy.ReasonTwins)
		}
	}
	args := c.pointerArgs(c.windowArgs(e.win, map[string]any{}), button(c.s), count(c.s))
	if e.win.Bounds != nil {
		args["x"], args["y"] = x-e.win.Bounds[0], y-e.win.Bounds[1]
	}
	cl := c.sendClick(ctx, x, y, args)
	if cl.err != nil {
		return failed(cl, "the background click")
	}
	c.rememberClick(e.win, x, y, cl.reply.Focused)
	a := c.pointerResult(cl, e.win, fmt.Sprintf("clicked %s at its centre in %q", e.describe(), e.win.Title), nil)
	a.Result.Status = protocol.StatusUnconfirmed
	if e.win.Space == "other" {
		a.Result.Notes = append(a.Result.Notes, "the element's window is on another Space; "+unseenInput)
	}
	return a
}

func (c *stepCtx) rememberClick(w view.RawWindow, x, y int, f *Focus) {
	c.x.mu.Lock()
	defer c.x.mu.Unlock()
	if c.x.bgClicks == nil {
		c.x.bgClicks = map[string]bgClick{}
	}
	c.x.bgClicks[w.Key] = bgClick{x: x, y: y, focus: f}
}

// point maps a step's point to whole desktop pixels: screen points as they
// are, window points from the named window's image (its title must be its
// own). Image points belong to the vision layer.
func (c *stepCtx) point(ctx context.Context, p protocol.Point, named *view.RawWindow) (int, int, *protocol.Error) {
	switch p.Space {
	case "", protocol.SpaceScreen:
		return round(p.X), round(p.Y), nil
	case protocol.SpaceWindow:
		if named == nil || named.Bounds == nil {
			return 0, 0, &protocol.Error{Code: protocol.ErrInvalid, Message: "a window point needs a named window with a frame; nothing was sent", Hint: "name the window, or give a screen point"}
		}
		return named.Bounds[0] + round(p.X), named.Bounds[1] + round(p.Y), nil
	}
	return 0, 0, &protocol.Error{Code: protocol.ErrUnavailable, Message: "image points need the vision layer, which this build does not have yet; nothing was sent", Hint: "give a screen point or a semantic target"}
}

// uniqueTitle refuses window-pixel points when the window's title, as the
// step named it, matches more than one window at the tier that picked it.
func (c *stepCtx) uniqueTitle(ctx context.Context) *protocol.Error {
	name := c.s.Window
	if t := c.pointTarget(); t != nil && t.Window != "" {
		name = t.Window
	}
	if name == "" {
		name = c.req.Window
	}
	if name == "" || refLike.MatchString(name) {
		return nil
	}
	listed, _ := c.windows(ctx)
	for tier := 0; tier < view.TitleTiers; tier++ {
		var hits []view.RawWindow
		for _, l := range listed {
			if view.TitleMatches(tier, l.Title, name) {
				hits = append(hits, l)
			}
		}
		if len(hits) == 0 {
			continue
		}
		if len(hits) == 1 {
			return nil
		}
		same := true
		for _, h := range hits {
			same = same && h.Title == hits[0].Title
		}
		if same {
			return &protocol.Error{Code: protocol.ErrTargetAmbiguous, Message: fmt.Sprintf("two windows are titled %q, so its window pixels are ambiguous; nothing was sent", hits[0].Title), Hint: "name a window with a title of its own, or give a screen point"}
		}
		return &protocol.Error{Code: protocol.ErrTargetAmbiguous, Message: fmt.Sprintf("%q matches %d windows, so its window pixels are ambiguous; nothing was sent", name, len(hits)), Hint: "use a longer title, or give a screen point"}
	}
	return nil
}

func (c *stepCtx) pointTarget() *protocol.Target {
	for _, t := range []*protocol.Target{c.s.Click, c.s.Scroll, c.s.Drag} {
		if t != nil {
			return t
		}
	}
	return nil
}

// named is the window a pointer step names: its target's window, else the
// step's window, app or pid, else the request's.
func (c *stepCtx) named(ctx context.Context, t protocol.Target) (*view.RawWindow, string, *protocol.Error) {
	if t.Window != "" {
		w, perr := c.window(ctx, t.Window, c.s.PID)
		return w, "", perr
	}
	return c.target(ctx)
}

// callerPoint names a point as the caller gave it.
func callerPoint(p protocol.Point) string {
	s := "(" + fmtNum(p.X) + ", " + fmtNum(p.Y) + ")"
	if p.Space == protocol.SpaceWindow {
		s += " in window pixels"
	}
	return s
}

// clickPoint clicks at a point.
func (c *stepCtx) clickPoint(ctx context.Context, t protocol.Target) policy.Attempt {
	if t.Point.Space == protocol.SpaceWindow {
		if perr := c.uniqueTitle(ctx); perr != nil {
			return refuse(perr, policy.ReasonPolicy)
		}
	}
	named, reason, perr := c.named(ctx, t)
	if perr != nil {
		return refuse(perr, reason)
	}
	px, py, perr := c.point(ctx, *t.Point, named)
	if perr != nil {
		return refuse(perr, policy.ReasonPolicy)
	}
	listed, perr := c.windows(ctx)
	if perr != nil {
		return refuse(perr, "")
	}
	allow := c.x.policy().Allow
	if named != nil {
		perr = allow.Window(named)
	} else {
		perr = allow.Points(listed, [2]int{px, py})
	}
	if perr != nil {
		return refuse(perr, policy.ReasonPolicy)
	}
	btn, n := button(c.s), count(c.s)
	var a policy.Attempt
	if !c.fg() {
		a = c.backgroundPointClick(ctx, named, listed, px, py, btn, n)
	} else {
		a = c.foregroundPointClick(ctx, named, listed, px, py, btn, n)
	}
	if a.Result.Status != protocol.StatusRefused {
		// Name the point as the caller gave it, then any scope note.
		scope := strings.TrimPrefix(a.Result.Evidence, fmt.Sprintf("clicked at (%d, %d)", px, py))
		a.Result.Evidence = "clicked at " + callerPoint(*t.Point) + scope
	}
	return a
}

// backgroundPointClick clicks a point in the named window, or in the window
// under it, without raising anything.
func (c *stepCtx) backgroundPointClick(ctx context.Context, named *view.RawWindow, listed []view.RawWindow, px, py int, btn string, n int) policy.Attempt {
	w := named
	if w == nil {
		top, err := view.WindowAt(listed, px, py)
		if err != nil {
			return refuse(&protocol.Error{Code: protocol.ErrWindowNotFound, Message: fmt.Sprintf("no window at (%d, %d) to click in the background; nothing was sent", px, py), Hint: "name the window"}, policy.ReasonDesktop)
		}
		w = &top
	}
	if securityPrompt(*w) {
		return refuse(securityError(*w), policy.ReasonPolicy)
	}
	note, perr := c.twins(ctx, w)
	if perr != nil {
		return refuse(perr, policy.ReasonTwins)
	}
	a := c.clickWindow(ctx, *w, px, py, btn, n, nil)
	if note != "" && a.Result.Status != protocol.StatusRefused {
		a.Result.Status = protocol.StatusUnconfirmed
		a.Result.Notes = append(a.Result.Notes, note)
	}
	if a.Result.Status == protocol.StatusRefused && policy.PointerRefused(a.Detail) {
		a.Reason = policy.ReasonPointer
		a.Result.Error.Message = fmt.Sprintf("window %q takes no background pointer input; nothing was sent", w.Title)
		a.Result.Error.Hint = "pointer input to it needs the foreground"
	}
	return a
}

// foregroundPointClick clicks a point in the foreground: an item of an open
// menu runs its action; otherwise the window on top there is raised and
// clicked, and a point another app's window covers goes to the target in
// the background.
func (c *stepCtx) foregroundPointClick(ctx context.Context, named *view.RawWindow, listed []view.RawWindow, px, py int, btn string, n int) policy.Attempt {
	if perr := c.x.policy().Activity.Check(); perr != nil {
		return refuse(perr, "")
	}
	hit, _, win, covered, hitErr := c.x.B.At(ctx, px, py)
	if hitErr == nil && hit != nil && hit.Role == "menuitem" && hit.HasState("showing") {
		if err := c.x.B.Perform(ctx, hit.Handle, "AXPress"); err != nil {
			return axFailure(element{node: *hit, h: hit.Handle}, err, "the menu item's action")
		}
		return confirmed(fmt.Sprintf("clicked at (%d, %d): ran the open menu's item %q", px, py, hit.Name))
	}
	if win != nil && securityPrompt(*win) {
		return refuse(securityError(*win), policy.ReasonPolicy)
	}
	if named != nil {
		return c.namedPointClick(ctx, *named, listed, px, py, btn, n)
	}
	top, topErr := view.WindowAt(listed, px, py)
	switch {
	case hitErr == nil && win != nil:
		target := listedTwin(listed, *win)
		if covered != nil && covered.PID != win.PID {
			return c.coveredClick(ctx, target, covered.App, px, py, btn, n)
		}
		return c.clickWindow(ctx, target, px, py, btn, n, nil)
	case hitErr != nil:
		// The hit test failed (an app with little accessibility): the window
		// server's list says what is on top.
		if topErr != nil {
			a := c.clickDesktop(ctx, px, py, btn, n)
			a.Result.Notes = append(a.Result.Notes, "the point could not be checked: no window is listed there")
			return a
		}
		front := c.front(ctx)
		if front == nil || front.PID == top.PID || !inside(front.Bounds, px, py) {
			return c.clickWindow(ctx, top, px, py, btn, n, nil)
		}
		return c.coveredClick(ctx, *front, top.App, px, py, btn, n)
	case topErr == nil:
		return c.clickWindow(ctx, top, px, py, btn, n, nil)
	}
	return c.clickDesktop(ctx, px, py, btn, n)
}

// listedTwin is the listed entry of a window a hit test reported.
func listedTwin(listed []view.RawWindow, w view.RawWindow) view.RawWindow {
	for _, l := range listed {
		if l.Key == w.Key && w.Key != "" {
			return l
		}
	}
	return w
}

// coveredClick sends a click whose point another app covers to the target
// window in the background, without moving the pointer.
func (c *stepCtx) coveredClick(ctx context.Context, w view.RawWindow, coverApp string, px, py int, btn string, n int) policy.Attempt {
	bg := *c
	bg.mode = protocol.InputBackground
	note := fmt.Sprintf("the point is covered by %s's window, so the click went to %q in the background without moving the pointer; confirm it by reading back", orApp(coverApp), w.Title)
	a := bg.clickWindow(ctx, w, px, py, btn, n, &note)
	if a.Result.Status == protocol.StatusRefused {
		a.Result.Error = &protocol.Error{
			Code:    protocol.ErrCovered,
			Message: fmt.Sprintf("%s's window covers the point in %q, which takes no background click; nothing was clicked", orApp(coverApp), w.Title),
			Hint:    "bring the window to the front first",
		}
		a.Reason = policy.ReasonPolicy
	}
	a.Result.Input = protocol.InputBackground
	return a
}

func orApp(app string) string {
	if app == "" {
		return "another app"
	}
	return app
}

// namedPointClick clicks a point of a named window in the foreground. The
// window is raised only when the point lies inside it and another listed
// window is on top there, and the click waits up to FrontWait for the cover
// to leave. A same-app window that is untitled counts as the target's own.
func (c *stepCtx) namedPointClick(ctx context.Context, w view.RawWindow, listed []view.RawWindow, px, py int, btn string, n int) policy.Attempt {
	if !inside(w.Bounds, px, py) {
		return c.clickDesktop(ctx, px, py, btn, n)
	}
	cover := coverAt(listed, w, px, py)
	if cover == nil {
		// Nothing covers the point: the driver's foreground click fronts
		// the window for the click itself.
		w.Active = true
		return c.clickWindow(ctx, w, px, py, btn, n, nil)
	}
	if perr := c.raise(ctx, w); perr != nil && perr.Code != protocol.ErrFocusMismatch {
		return refuse(perr, "")
	}
	deadline := c.x.now().Add(FrontWait)
	for cover != nil {
		if !c.x.now().Before(deadline) {
			break
		}
		if err := c.x.sleep(ctx, FrontPoll); err != nil {
			return refuse(asProtocol(err), "")
		}
		fresh, perr := c.refresh(ctx)
		if perr != nil {
			return refuse(perr, "")
		}
		cover = coverAt(fresh, w, px, py)
	}
	if cover != nil && cover.PID != w.PID {
		return c.coveredClick(ctx, w, cover.App, px, py, btn, n)
	}
	w.Active = true // raised, or covered only by its own app
	return c.clickWindow(ctx, w, px, py, btn, n, nil)
}

// coverAt is the listed window on top of w at a point, if another; an
// untitled window of w's app counts as w's own.
func coverAt(listed []view.RawWindow, w view.RawWindow, px, py int) *view.RawWindow {
	top, err := view.WindowAt(listed, px, py)
	if err != nil || top.Key == w.Key {
		return nil
	}
	if top.PID == w.PID && (top.Title == "" || top.OwnerKey == w.Key) {
		return nil
	}
	return &top
}

// ---------------------------------------------------------------------------
// Scroll and drag

// pointOf is a target's desktop point: a point, or an element's centre.
func (c *stepCtx) pointOf(ctx context.Context, t protocol.Target, named *view.RawWindow) (int, int, *view.RawWindow, *protocol.Error) {
	if t.Point != nil {
		x, y, perr := c.point(ctx, *t.Point, named)
		return x, y, named, perr
	}
	e, perr := c.element(ctx, t)
	if perr != nil {
		return 0, 0, nil, perr
	}
	x, y, ok := center(e.node.Bounds)
	if !ok {
		return 0, 0, nil, &protocol.Error{Code: protocol.ErrTargetNotFound, Message: e.describe() + " shows no point; nothing was sent"}
	}
	return x, y, &e.win, nil
}

// pointerWindow is the window a pointer step at a point goes to.
func (c *stepCtx) pointerWindow(listed []view.RawWindow, named *view.RawWindow, px, py int) (*view.RawWindow, *protocol.Error) {
	if named != nil {
		return named, nil
	}
	top, err := view.WindowAt(listed, px, py)
	if err != nil {
		return nil, asProtocol(err)
	}
	return &top, nil
}

// scroll wheels at a point (default 3 notches), or without a point sends
// scroll keys to the target or front window.
func (c *stepCtx) scroll(ctx context.Context) policy.Attempt {
	t := *c.s.Scroll
	if t.Point != nil && t.Point.Space == protocol.SpaceWindow {
		if perr := c.uniqueTitle(ctx); perr != nil {
			return refuse(perr, policy.ReasonPolicy)
		}
	}
	dir := strings.ToLower(strings.TrimSpace(c.s.Direction))
	if dir == "" {
		dir = "down"
	}
	switch dir {
	case "up", "down", "left", "right":
	default:
		return refuse(&protocol.Error{Code: protocol.ErrInvalid, Message: "direction is up, down, left or right"}, policy.ReasonPolicy)
	}
	amount := c.s.Amount
	if amount <= 0 {
		amount = 3
	}
	if amount > 50 {
		amount = 50
	}
	listed, perr := c.windows(ctx)
	if perr != nil {
		return refuse(perr, "")
	}
	named, reason, perr := c.named(ctx, t)
	if perr != nil {
		return refuse(perr, reason)
	}
	args := map[string]any{"direction": dir, "amount": amount}
	var w *view.RawWindow
	at := ""
	if t.Ref != "" && refLike.MatchString(t.Ref) && t.Ref[0] == 'w' {
		if w, perr = c.window(ctx, t.Ref, 0); perr != nil {
			return refuse(perr, "")
		}
	} else if t.Point != nil || t.Ref != "" || t.Select != "" {
		px, py, ew, perr := c.pointOf(ctx, t, named)
		if perr != nil {
			return refuse(perr, policy.ReasonPolicy)
		}
		if ew != nil {
			named = ew
		}
		if perr := c.x.policy().Allow.Points(listed, [2]int{px, py}); perr != nil && named == nil {
			return refuse(perr, policy.ReasonPolicy)
		}
		if w, perr = c.pointerWindow(listed, named, px, py); perr != nil {
			if !c.fg() {
				return refuse(perr, policy.ReasonDesktop)
			}
			args = c.desktopArgs(args)
			args["x"], args["y"] = px, py
		}
		if w != nil && w.Bounds != nil {
			args["x"], args["y"] = px-w.Bounds[0], py-w.Bounds[1]
		}
		at = fmt.Sprintf(" at (%d, %d)", px, py)
	}
	if w == nil && args["scope"] == nil {
		w = named
		if w == nil {
			w = c.front(ctx)
		}
		if w == nil {
			return refuse(policy.BackgroundTarget("scroll", false), policy.ReasonNoWindow)
		}
	}
	if w != nil {
		if perr := c.x.policy().Allow.Window(w); perr != nil {
			return refuse(perr, policy.ReasonPolicy)
		}
		if c.fg() {
			if perr := c.raise(ctx, *w); perr != nil {
				return refuse(perr, "")
			}
		}
		args = c.windowArgs(*w, args)
	} else if perr := c.x.policy().Activity.Check(); perr != nil {
		return refuse(perr, "")
	}
	cl := driverCall(ctx, c.x.B, "scroll", args)
	if cl.sent {
		c.own()
	}
	if cl.err != nil {
		a := failed(cl, "the scroll")
		if a.Reason == policy.ReasonPointer && !c.fg() {
			a.Result.Error.Message = "the window takes no background pointer input; nothing was sent"
		}
		return a
	}
	ev := fmt.Sprintf("scrolled %s %d%s", dir, amount, at)
	if w == nil {
		return confirmedOr(cl, ev)
	}
	return c.pointerResult(cl, *w, ev+fmt.Sprintf(" in %q", w.Title), nil)
}

// drag presses at the start, moves and releases at the end; the allowlist
// gates both ends.
func (c *stepCtx) drag(ctx context.Context) policy.Attempt {
	for _, p := range []*protocol.Target{c.s.Drag, c.s.To} {
		if p != nil && p.Point != nil && p.Point.Space == protocol.SpaceWindow {
			if perr := c.uniqueTitle(ctx); perr != nil {
				return refuse(perr, policy.ReasonPolicy)
			}
		}
	}
	if c.s.To == nil {
		return refuse(&protocol.Error{Code: protocol.ErrInvalid, Message: "drag needs to: the end point or element"}, policy.ReasonPolicy)
	}
	listed, perr := c.windows(ctx)
	if perr != nil {
		return refuse(perr, "")
	}
	named, reason, perr := c.named(ctx, *c.s.Drag)
	if perr != nil {
		return refuse(perr, reason)
	}
	fx, fy, fw, perr := c.pointOf(ctx, *c.s.Drag, named)
	if perr != nil {
		return refuse(perr, policy.ReasonPolicy)
	}
	tx, ty, _, perr := c.pointOf(ctx, *c.s.To, named)
	if perr != nil {
		return refuse(perr, policy.ReasonPolicy)
	}
	allow := c.x.policy().Allow
	if named != nil {
		perr = allow.Window(named)
	} else {
		perr = allow.Points(listed, [2]int{fx, fy}, [2]int{tx, ty})
	}
	if perr != nil {
		return refuse(perr, policy.ReasonPolicy)
	}
	if fw != nil {
		named = fw
	}
	w, perr := c.pointerWindow(listed, named, fx, fy)
	if perr != nil {
		return refuse(&protocol.Error{Code: protocol.ErrWindowNotFound, Message: fmt.Sprintf("no window at (%d, %d) to drag in; nothing was sent", fx, fy)}, policy.ReasonDesktop)
	}
	if c.fg() {
		if perr := c.raise(ctx, *w); perr != nil {
			return refuse(perr, "")
		}
	}
	args := c.windowArgs(*w, map[string]any{})
	ox, oy := 0, 0
	if w.Bounds != nil {
		ox, oy = w.Bounds[0], w.Bounds[1]
	}
	args["from_x"], args["from_y"], args["to_x"], args["to_y"] = fx-ox, fy-oy, tx-ox, ty-oy
	if !isLeft(c.s.Button) {
		args["button"] = button(c.s)
	}
	if len(c.s.Mods) > 0 {
		args = c.pointerArgs(args, button(c.s), 1)
	}
	cl := driverCall(ctx, c.x.B, "drag", args)
	if cl.sent {
		c.own()
	}
	if cl.err != nil {
		return failed(cl, "the drag")
	}
	return c.pointerResult(cl, *w, fmt.Sprintf("dragged from (%d, %d) to (%d, %d) in %q", fx, fy, tx, ty, w.Title), nil)
}

func (x *Executor) markCenter(b *[4]int) {
	if cx, cy, ok := center(b); ok {
		x.markClick(cx, cy)
	}
}
