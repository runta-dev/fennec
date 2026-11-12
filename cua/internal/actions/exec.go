package actions

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/driver"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// Click spacing: two clicks at the same point (within SpacingPoints) are at
// least ClickSpacing apart, past the double-click interval.
const (
	ClickSpacing  = 500 * time.Millisecond
	SpacingPoints = 16
)

// Waits.
const (
	FocusWait    = time.Second            // a type step's focus condition
	FocusPoll    = 100 * time.Millisecond // how often it is re-read
	FrontWait    = 500 * time.Millisecond // a raised window coming to the front
	FrontPoll    = 50 * time.Millisecond
	CloseWait    = time.Second
	SettleBudget = 100 * time.Millisecond // after confirmed typing
	SettlePoll   = 10 * time.Millisecond
)

// Executor runs single steps.
type Executor struct {
	B      Backend
	Refs   *view.Registry
	Policy *policy.Policy
	// Now and Sleep are the clock; time.Now and a context-aware sleep by default.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error

	mu        sync.Mutex
	lastClick clickMark
	bgClicks  map[string]bgClick // window key -> the last background click into it
}

type clickMark struct {
	x, y int
	at   time.Time
	set  bool
}

type bgClick struct {
	x, y  int
	focus *Focus
}

// Request is what a step inherits from its act request.
type Request struct {
	Window string
	Input  string
	// WholeText sends a type step's text, line breaks included, in one
	// typing call instead of line by line with Return keys between.
	WholeText bool
}

func (x *Executor) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

func (x *Executor) sleep(ctx context.Context, d time.Duration) error {
	if x.Sleep != nil {
		return x.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (x *Executor) policy() *policy.Policy {
	if x.Policy == nil {
		p, _ := policy.New(policy.Config{}, nil)
		x.Policy = p
	}
	return x.Policy
}

// autoInput are the steps auto input tries in the background first.
var autoInput = map[string]bool{"key": true, "type": true, "click": true, "scroll": true, "drag": true, "press": true}

// Step runs one step and answers its result.
func (x *Executor) Step(ctx context.Context, req Request, s protocol.Step, index int) protocol.StepResult {
	start := x.now()
	kind, err := s.Kind()
	var a policy.Attempt
	if err != nil {
		a = refuse(&protocol.Error{Code: protocol.ErrInvalid, Message: err.Error()}, policy.ReasonPolicy)
	} else if mode, perr := policy.StepMode(x.policy().Input, req.Input, s.Input); perr != nil {
		a = refuse(perr, policy.ReasonPolicy)
	} else {
		if x.Refs != nil {
			release := x.Refs.Hold()
			defer release()
		}
		run := func(m string) func() policy.Attempt {
			return func() policy.Attempt {
				c := &stepCtx{x: x, req: req, s: s, kind: kind, mode: m}
				out := c.run(ctx)
				if out.Result.Input == "" {
					out.Result.Input = m
				}
				return out
			}
		}
		switch {
		case mode == protocol.InputAuto && autoInput[kind]:
			a = policy.Auto(kind, run(protocol.InputBackground), run(protocol.InputForeground))
		case mode == protocol.InputAuto:
			a = run(protocol.InputBackground)()
		default:
			a = run(mode)()
		}
	}
	r := a.Result
	r.Index, r.Action = index, kind
	r.Ms = int(x.now().Sub(start).Milliseconds())
	switch r.Status {
	case protocol.StatusConfirmed:
		r.OK = true
	case protocol.StatusUnconfirmed:
		r.OK = r.Error == nil
		no := false
		r.Retry = &no
	default:
		r.Status = protocol.StatusRefused
		r.OK = false
	}
	if s.Secret && s.Type != nil {
		r = redact(r, *s.Type)
	}
	return r
}

// redact removes a secret from everything a result says.
func redact(r protocol.StepResult, secret string) protocol.StepResult {
	if strings.TrimSpace(secret) == "" {
		return r
	}
	hide := func(s string) string { return strings.ReplaceAll(s, secret, "[secret]") }
	r.Evidence = hide(r.Evidence)
	for i := range r.Notes {
		r.Notes[i] = hide(r.Notes[i])
	}
	if r.Error != nil {
		e := *r.Error
		e.Message, e.Hint = hide(e.Message), hide(e.Hint)
		r.Error = &e
	}
	return r
}

// ---------------------------------------------------------------------------
// Outcomes

func refuse(perr *protocol.Error, reason string) policy.Attempt {
	return policy.Attempt{Result: protocol.StepResult{Status: protocol.StatusRefused, Error: perr}, Reason: reason}
}

func confirmed(evidence string, notes ...string) policy.Attempt {
	return policy.Attempt{Result: protocol.StepResult{Status: protocol.StatusConfirmed, Evidence: evidence, Notes: notes}}
}

// unconfirmed is a step that was sent but not proven; with perr it also
// failed part way.
func unconfirmed(evidence string, perr *protocol.Error, notes ...string) policy.Attempt {
	return policy.Attempt{Result: protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: evidence, Error: perr, Notes: notes}}
}

func noDriver() *protocol.Error {
	return &protocol.Error{Code: protocol.ErrUnavailable, Message: "this step needs cua-driver, which is not bundled with this build; nothing was sent", Hint: "use press, set, menu, focus or close, which work through accessibility alone"}
}

// failed turns a driver call that did not succeed into an attempt: a stated
// refusal sent nothing; anything else may have landed and is never resent.
func failed(c call, what string) policy.Attempt {
	if errors.Is(c.err, driver.ErrNoDriver) {
		return refuse(noDriver(), policy.ReasonPolicy)
	}
	if !c.sent {
		code := protocol.ErrUnavailable
		switch c.reply.RefusalCode() {
		case "hit_mismatch":
			code = protocol.ErrHitMismatch
		case "security_prompt":
			code = protocol.ErrSecurityPrompt
		}
		if strings.Contains(strings.ToLower(c.msg), "security") && strings.Contains(strings.ToLower(c.msg), "prompt") {
			code = protocol.ErrSecurityPrompt
		}
		reason := policy.ReasonDriver
		if policy.PointerRefused(c.msg) {
			reason = policy.ReasonPointer
		}
		a := refuse(&protocol.Error{Code: code, Message: what + " was refused: " + c.msg + "; nothing was sent", Hint: c.reply.Hint}, reason)
		a.Detail = c.msg
		return a
	}
	code := protocol.ErrInternal
	if errors.Is(c.err, context.DeadlineExceeded) || errors.Is(c.err, context.Canceled) {
		code = protocol.ErrTimeout
	}
	return unconfirmed("", &protocol.Error{
		Code:    code,
		Message: what + " failed after it may have been sent: " + c.msg,
		Hint:    "do not resend unchecked: read the target back first",
	})
}

// ---------------------------------------------------------------------------
// One step

type stepCtx struct {
	x      *Executor
	req    Request
	s      protocol.Step
	kind   string
	mode   string
	locked bool
	app    string // the target app, for locked-screen notes
	listed []view.RawWindow
	read   bool
}

func (c *stepCtx) fg() bool { return c.mode == protocol.InputForeground }

func (c *stepCtx) delivery() string {
	if c.fg() {
		return protocol.InputForeground
	}
	return protocol.InputBackground
}

func (c *stepCtx) run(ctx context.Context) policy.Attempt {
	return c.lockedNote(c.dispatch(ctx))
}

func (c *stepCtx) dispatch(ctx context.Context) policy.Attempt {
	if c.x.B.ScreenLocked(ctx) {
		if a, stop := c.lockedScreen(ctx); stop {
			return a
		}
	}
	switch c.kind {
	case "press":
		return c.press(ctx)
	case "set":
		return c.set(ctx)
	case "click":
		return c.click(ctx)
	case "type":
		return c.typeText(ctx)
	case "key":
		return c.key(ctx)
	case "scroll":
		return c.scroll(ctx)
	case "drag":
		return c.drag(ctx)
	case "menu":
		return c.menu(ctx)
	case "focus":
		return c.focus(ctx)
	case "close":
		return c.closeWindow(ctx)
	case "launch":
		return c.launch(ctx)
	}
	return refuse(&protocol.Error{Code: protocol.ErrUnavailable, Message: c.kind + " steps run in the plan executor, which this build does not have yet; nothing was done"}, policy.ReasonPolicy)
}

// lockedScreen applies the locked-screen policy; stop is true when the step
// ends here.
func (c *stepCtx) lockedScreen(ctx context.Context) (policy.Attempt, bool) {
	var keys []string
	if c.kind == "key" {
		keys, _ = KeyNames(c.s.Key)
	}
	app := c.s.App
	if w, _, _ := c.target(ctx); w != nil {
		app = w.App
	}
	if perr := c.x.policy().Locked(c.kind, keys, c.mode, app, c.x.B.LockedDelivery()); perr != nil {
		return refuse(perr, policy.ReasonLocked), true
	}
	c.locked, c.app = true, app
	return policy.Attempt{}, false
}

// windows lists the windows this step may see, once per step.
func (c *stepCtx) windows(ctx context.Context) ([]view.RawWindow, *protocol.Error) {
	if c.read {
		return c.listed, nil
	}
	raw, err := c.x.B.Windows(ctx)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.ErrInternal, Message: "listing windows: " + err.Error()}
	}
	c.listed = c.x.policy().Allow.Filter(view.ListWindows(raw, c.x.policy().Input))
	c.read = true
	return c.listed, nil
}

// refresh lists again.
func (c *stepCtx) refresh(ctx context.Context) ([]view.RawWindow, *protocol.Error) {
	c.read = false
	return c.windows(ctx)
}

var refLike = regexp.MustCompile(`^[ew][0-9]+$`)

// window resolves a window name: a window ref, or a title (with pid).
func (c *stepCtx) window(ctx context.Context, name string, pid int) (*view.RawWindow, *protocol.Error) {
	listed, perr := c.windows(ctx)
	if perr != nil {
		return nil, perr
	}
	name = strings.TrimSpace(name)
	if refLike.MatchString(name) {
		if name[0] == 'e' {
			return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: name + " is an element, not a window; nothing was sent", Hint: "name a window by its ref (w3) or title"}
		}
		if c.x.Refs == nil {
			return nil, view.StaleRef(name)
		}
		e, err := c.x.Refs.Lookup(name)
		if err != nil {
			return nil, asProtocol(err)
		}
		for i := range listed {
			if listed[i].Key == e.WindowKey {
				w := listed[i]
				return &w, nil
			}
		}
		return nil, &protocol.Error{Code: protocol.ErrWindowNotFound, Message: "window " + name + " is gone; nothing was sent", Hint: "observe scope desktop for the open windows"}
	}
	if w, ok := c.spacePick(listed, name, pid); ok {
		return &w, nil
	}
	w, err := view.PickWindow(listed, name, pid)
	if err != nil {
		return nil, asProtocol(err)
	}
	if w.Stale {
		return nil, asProtocol(view.Actionable(w, protocol.InputForeground))
	}
	return &w, nil
}

// target is the step's window: its window, app or pid, else the request's
// window; nil when nothing names one.
func (c *stepCtx) target(ctx context.Context) (*view.RawWindow, string, *protocol.Error) {
	switch {
	case c.s.Window != "":
		w, perr := c.window(ctx, c.s.Window, c.s.PID)
		return w, "", perr
	case c.s.App != "":
		listed, perr := c.windows(ctx)
		if perr != nil {
			return nil, "", perr
		}
		w, err := view.PickApp(listed, c.s.App)
		if err != nil {
			pe := asProtocol(err)
			if pe.Code == protocol.ErrWindowNotFound {
				pe.Message = fmt.Sprintf("the app %q has no window, so nothing was sent", c.s.App)
				return nil, policy.ReasonAppNoWindow, pe
			}
			return nil, "", pe
		}
		return &w, "", nil
	case c.s.PID != 0:
		w, perr := c.window(ctx, "", c.s.PID)
		if perr != nil && perr.Code == protocol.ErrWindowNotFound {
			perr.Message = fmt.Sprintf("process %d has no window, so nothing was sent", c.s.PID)
			return nil, policy.ReasonAppNoWindow, perr
		}
		return w, "", perr
	case c.req.Window != "":
		w, perr := c.window(ctx, c.req.Window, 0)
		return w, "", perr
	}
	return nil, "", nil
}

// front is the active listed window, if any.
func (c *stepCtx) front(ctx context.Context) *view.RawWindow {
	listed, _ := c.windows(ctx)
	for i := range listed {
		if listed[i].Active {
			w := listed[i]
			return &w
		}
	}
	return nil
}

func asProtocol(err error) *protocol.Error {
	if err == nil {
		return nil
	}
	var pe *protocol.Error
	if errors.As(err, &pe) {
		cp := *pe
		return &cp
	}
	return &protocol.Error{Code: protocol.ErrInternal, Message: err.Error()}
}

// unseen says why a window may not repaint: another Space, minimized, hidden.
func unseen(w *view.RawWindow) bool {
	return w != nil && (w.Space == "other" || w.Hidden != "")
}

// Hints for unconfirmed input to a window nobody sees.
const (
	unseenTyped = "the window is not on screen (another Space, minimized or hidden), so the text cannot be read back and may have landed even if an image looks unchanged: do not type it again, and have the user confirm the text before Return or Send"
	unseenInput = "the window is not on screen (another Space, minimized or hidden), so the input cannot be read back and may have landed even if an image looks unchanged: do not resend it; confirm its effect by a trustworthy read-back or by asking the user"
	twinNote    = "a window with the same title is on another Space; the input went to the one the title picks, and the app may not repaint an unseen window, so do not send it again"
)

// spaceTwins counts a window's same-titled twins of its app here and on
// other Spaces, itself included.
func spaceTwins(listed []view.RawWindow, w view.RawWindow) (here, there int, sameFrame bool) {
	for _, l := range listed {
		if l.Title != w.Title || l.PID != w.PID {
			continue
		}
		if l.Space == "other" {
			there++
		} else {
			here++
		}
		if l.Key != w.Key && l.Bounds != nil && w.Bounds != nil && *l.Bounds == *w.Bounds {
			sameFrame = true
		}
	}
	return
}

// twins refuses background input to title twins split across Spaces with
// several on each side, and notes a twin on the other side otherwise.
func (c *stepCtx) twins(ctx context.Context, w *view.RawWindow) (note string, perr *protocol.Error) {
	if w == nil || c.fg() || c.s.Window == "" || refLike.MatchString(c.s.Window) {
		return "", nil
	}
	listed, _ := c.windows(ctx)
	here, there, same := spaceTwins(listed, *w)
	switch {
	case here >= 2 && there >= 2:
		return "", &protocol.Error{
			Code:    protocol.ErrTargetAmbiguous,
			Message: fmt.Sprintf("%d windows titled %q are on this Space and %d on other Spaces; nothing was sent", here, w.Title, there),
			Hint:    "name the window by its ref",
		}
	case here >= 1 && there >= 1:
		if same && c.kind == "type" {
			return "", &protocol.Error{Code: protocol.ErrTargetAmbiguous, Message: fmt.Sprintf("two windows titled %q, one on another Space, cannot be told apart; nothing was typed", w.Title), Hint: "name the window by its ref"}
		}
		return twinNote, nil
	}
	return "", nil
}

// windowID is the driver's window id of a window, or a sheet's owner's.
func windowID(w view.RawWindow, listed []view.RawWindow) (int, bool) {
	if id, ok := cgID(w.Key); ok {
		return id, true
	}
	for _, l := range listed {
		if l.Key == w.OwnerKey && w.OwnerKey != "" {
			return cgID(l.Key)
		}
	}
	return 0, false
}

func cgID(key string) (int, bool) {
	s, ok := strings.CutPrefix(key, "cg:")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

// windowArgs addresses a window in a driver call.
func (c *stepCtx) windowArgs(w view.RawWindow, args map[string]any) map[string]any {
	listed := c.listed
	args["pid"] = w.PID
	if id, ok := windowID(w, listed); ok {
		args["window_id"] = id
	}
	args["delivery_mode"] = c.delivery()
	if c.locked {
		args["locked_input"] = true
	}
	return args
}

// desktopArgs addresses the desktop (the frontmost app) in a driver call.
func (c *stepCtx) desktopArgs(args map[string]any) map[string]any {
	args["scope"] = "desktop"
	args["delivery_mode"] = c.delivery()
	if c.locked {
		args["locked_input"] = true
	}
	return args
}

// raise brings a named window to the front for foreground input and checks
// that it came; the user-activity check applies, but fronting is not own
// input.
func (c *stepCtx) raise(ctx context.Context, w view.RawWindow) *protocol.Error {
	if perr := c.x.policy().Activity.Check(); perr != nil {
		return perr
	}
	if w.Active {
		return nil
	}
	if err := c.x.B.Raise(ctx, w); err != nil {
		return &protocol.Error{Code: protocol.ErrWindowNotFound, Message: fmt.Sprintf("window %q could not be brought to the front: %v; nothing was sent", w.Title, err)}
	}
	deadline := c.x.now().Add(FrontWait)
	for {
		listed, perr := c.refresh(ctx)
		if perr != nil {
			return perr
		}
		var front *view.RawWindow
		for i := range listed {
			if listed[i].Active {
				front = &listed[i]
				break
			}
		}
		if front != nil && front.Key == w.Key {
			return nil
		}
		if !c.x.now().Before(deadline) {
			name := "no window"
			if front != nil {
				name = fmt.Sprintf("%q of %s", front.Title, front.App)
			}
			return &protocol.Error{
				Code:    protocol.ErrFocusMismatch,
				Message: fmt.Sprintf("window %q is not frontmost (the front window is %s); nothing was sent", w.Title, name),
				Hint:    "focus the window first, or use background input",
			}
		}
		if err := c.x.sleep(ctx, FrontPoll); err != nil {
			return asProtocol(err)
		}
	}
}

// own records that foreground input just ended.
func (c *stepCtx) own() {
	if c.fg() {
		c.x.policy().Activity.Own()
	}
}

// lockedNote marks input sent through a locked screen.
func (c *stepCtx) lockedNote(a policy.Attempt) policy.Attempt {
	if !c.locked || a.Result.Status == protocol.StatusRefused {
		return a
	}
	if a.Result.Status == protocol.StatusConfirmed {
		a.Result.Status = protocol.StatusUnconfirmed
	}
	a.Result.Notes = append(a.Result.Notes, policy.LockedDelivered(c.app))
	return a
}

// round is a whole desktop pixel; negative coordinates (displays left of or
// above the main one) are kept.
func round(v float64) int { return int(math.Round(v)) }

func center(b *[4]int) (int, int, bool) {
	if b == nil || b[2] <= 0 || b[3] <= 0 {
		return 0, 0, false
	}
	return b[0] + b[2]/2, b[1] + b[3]/2, true
}

func inside(b *[4]int, x, y int) bool {
	return b != nil && x >= b[0] && y >= b[1] && x < b[0]+b[2] && y < b[1]+b[3]
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// spacePick picks among title twins split across Spaces for background
// input: the front-most one on this Space. twins then refuses or notes it.
func (c *stepCtx) spacePick(listed []view.RawWindow, name string, pid int) (view.RawWindow, bool) {
	if c.fg() || name == "" {
		return view.RawWindow{}, false
	}
	for tier := 0; tier < 2; tier++ {
		var here, there []view.RawWindow
		for _, l := range listed {
			if (pid == 0 || l.PID == pid) && view.TitleMatches(tier, l.Title, name) {
				if l.Space == "other" {
					there = append(there, l)
				} else {
					here = append(here, l)
				}
			}
		}
		if len(here) == 0 && len(there) == 0 {
			continue
		}
		if len(here) == 0 || len(there) == 0 {
			return view.RawWindow{}, false
		}
		return here[0], true
	}
	return view.RawWindow{}, false
}
