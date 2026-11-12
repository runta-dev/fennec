// Package plan is fennec-cua's act executor: it runs a plan's steps in order
// under one desk turn, resolves targets (refs, selectors, DOM ids, recorded
// fingerprints, model picks and visual grounding), sends each step through
// actions.Executor, checks expectations, web pages and recorded hand-offs,
// and stops at the first surprise with what ran, what is left and why.
// Passing plans extend the session chain and save as flows (internal/flows).
package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/flows"
	"fennec/cua/internal/models"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/record"
	"fennec/cua/internal/view"
	"fennec/cua/internal/vision"
)

// Plan bounds.
const (
	DefaultTimeout = 90 * time.Second
	MaxTimeout     = 110 * time.Second
	// LateWait is how long a step waits for a target that is not there yet
	// (a dialog the step before opened, a tree that fills late).
	LateWait = 2 * time.Second
	// FrontWait is how long an expected front window may take (expect
	// front, recorded after windows).
	FrontWait = 5 * time.Second
	// ExpectWait is the default wait of an expectation.
	ExpectWait = 5 * time.Second
	// HungWait is how long a front app that stopped answering may take to
	// answer again.
	HungWait = 2 * time.Second
	// EndWait is the end-of-plan wait for a window the last step opened.
	EndWait = time.Second
	// LaunchWait bounds the wait for a launched app's window.
	LaunchWait = 8 * time.Second
	// Poll is how often waits read the desktop again.
	Poll = 100 * time.Millisecond
)

// Models answers the executor's model calls (models.Client in production).
type Models interface {
	Decide(ctx context.Context, args models.DecideArgs) (*models.Decided, error)
	Ground(ctx context.Context, args models.GroundArgs) (*models.Grounded, error)
	Ask(ctx context.Context, args models.AskArgs) (*models.Asked, error)
}

// Shot is a capture of the whole desktop for model checks.
type Shot struct {
	Frame *vision.Frame
	// Origin is the desktop point of pixel (0, 0); negative on desktops
	// that span displays left of or above the main one.
	Origin [2]float64
	// Scale is desktop units per pixel.
	Scale float64
}

// Screen captures the desktop; nil means the executor reads no pixels.
type Screen interface {
	Shot(ctx context.Context) (*Shot, error)
}

// Executor runs plans.
type Executor struct {
	X *actions.Executor
	// Models resolves targets names cannot and checks visual expectations;
	// nil turns those fallbacks off.
	Models Models
	// VisionModel says the decide model answers screenshots (a verify model
	// is configured): picks are made on an annotated screenshot first and
	// grounded points are checked visually.
	VisionModel bool
	// NoRingCheck turns off the full-screen ring stage of the grounded
	// check.
	NoRingCheck bool
	Screen      Screen
	// Sessions maps image-space points by the screenshot of their view.
	Sessions *vision.Sessions
	Chains   *flows.Chains
	// Store receives one run log entry per plan.
	Store *flows.Store
	// Acting counts the plan's own input, which recordings leave out.
	Acting   *record.Acting
	Observer engine.Observer
	// WebTree turns a browser's page tree on and says whether it was on.
	WebTree func(ctx context.Context, pid int) (wasOn bool, err error)
	// WindowChange is the daemon's window-change wait: it calls list each time
	// the on-screen windows may have changed (an accessibility observer or
	// a cheap window fingerprint), until list reports the wait is over or
	// limit passes, and reports whether list ended it. nil polls the window
	// list every Poll.
	WindowChange func(ctx context.Context, limit time.Duration, list func(context.Context) (bool, error)) (bool, error)
	// WindowImage captures one window of process pid (by window key) and
	// answers the image's size in pixels; a background click with a button,
	// count or modifiers aims in those pixels. nil aims in desktop units.
	WindowImage func(ctx context.Context, pid int, key string) (w, h int, err error)
	Log         func(format string, args ...any)
	Now         func() time.Time
	Sleep       func(ctx context.Context, d time.Duration) error
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Executor) sleep(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, d)
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

func (e *Executor) policy() *policy.Policy {
	if e.X.Policy == nil {
		p, _ := policy.New(policy.Config{}, nil)
		e.X.Policy = p
	}
	return e.X.Policy
}

// Tool answers the act tool: steps are checked as sent, before anything
// is decoded or done.
func (e *Executor) Tool() engine.Tool {
	return func(ctx context.Context, call *engine.Call, args json.RawMessage) (any, *protocol.Error) {
		if perr := CheckRaw(args); perr != nil {
			return nil, perr
		}
		var req protocol.ActRequest
		if perr := engine.Decode(args, &req); perr != nil {
			return nil, perr
		}
		res, perr := e.Run(ctx, call, req, engine.PlanOptions{})
		if res == nil {
			return nil, perr
		}
		return res, nil
	}
}

// planRun is one plan in progress.
type planRun struct {
	e     *Executor
	call  *engine.Call
	req   protocol.ActRequest
	opt   engine.PlanOptions
	raw   []protocol.Step // as sent, for remaining steps
	steps []protocol.Step // filled
	input string          // the configured mode narrowed by the request
	res   *protocol.ActResult

	win        *view.RawWindow // the plan window, tracked by key
	gone       string          // the title of a plan window that closed
	doubt      string          // why background input cannot tell the plan's window
	fg         bool            // an auto plan took the foreground
	startFront view.RawWindow  // the front window at the start

	pending    []pendingStep    // sent, unconfirmed, waiting for a read-back
	axOnly     []int            // proven only by an accessibility action's reply
	report     bool             // stopped on a recorded check
	unsaved    bool             // a target only vision found
	lastBefore []view.RawWindow // the window list before the current step
	// known are the titles the plan expects in front: those listed at the
	// start and those a launch or wait step took. vetted are the front
	// windows (by key, pid and title) the plan has read; kept is the tree
	// the current step's vetting read, which serves the step's first read.
	known  map[string]bool
	vetted map[string]bool
	kept   *keptTree
	// pressed is the front window when the step before sent a shortcut,
	// which may open a window late.
	pressed *view.RawWindow
	// late is pressed for the step after the shortcut.
	late    *view.RawWindow
	timeout time.Duration
}

type pendingStep struct {
	index int
	note  string
	// background marks a note a background step left.
	background bool
}

// stop is a step that ends the plan.
type stop struct {
	err *protocol.Error
	// recorded marks a stop on a recorded check, which carries a stop
	// report.
	recorded bool
}

// Run runs a plan. It answers the plan's result even when the plan stopped,
// and an error alone only when the plan could not start.
func (e *Executor) Run(ctx context.Context, call *engine.Call, req protocol.ActRequest, opt engine.PlanOptions) (*protocol.ActResult, *protocol.Error) {
	if perr := Validate(req.Steps, req.Params); perr != nil {
		return nil, perr
	}
	input, perr := policy.StepMode(e.policy().Input, req.Input)
	if perr != nil {
		return nil, perr
	}
	steps := make([]protocol.Step, len(req.Steps))
	for i, s := range req.Steps {
		f, err := flows.Fill(s, req.Params)
		if err != nil {
			return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: fmt.Sprintf("step %d: %v; nothing ran", i, err)}
		}
		steps[i] = f
	}
	timeout := DefaultTimeout
	if req.TimeoutS > 0 {
		timeout = min(time.Duration(req.TimeoutS)*time.Second, MaxTimeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tally := &models.Tally{}
	ctx = models.WithTally(ctx, tally)

	target := req.Window
	if target == "" {
		target = "the focused window"
	}
	release, err := e.policy().Gate.Turn(ctx, turnOf(call), steps, target)
	if err != nil {
		return nil, engine.AsProtocol(err)
	}
	defer release()

	// A list kept from another call never serves this one.
	e.dropLists()
	r := &planRun{e: e, call: call, req: req, opt: opt, raw: req.Steps, steps: steps, input: input, timeout: timeout,
		res:   &protocol.ActResult{OK: true, Steps: []protocol.StepResult{}},
		known: map[string]bool{}, vetted: map[string]bool{}}
	if listed, err := r.list(ctx); err == nil {
		if f := frontOf(listed); f != nil {
			r.startFront = *f
		}
		for _, w := range listed {
			r.known[w.Title] = true
		}
		r.adoptStart(listed)
	}
	var chain *flows.Run
	if e.Chains != nil && e.Chains.Store != nil {
		chain, _ = e.Chains.Begin(flows.Plan{Steps: req.Steps, Params: req.Params, Flow: opt.Flow, Save: opt.Save,
			Front: flows.Window{Title: r.startFront.Title, PID: r.startFront.PID, Dialog: dialogRole(r.startFront.Role)}})
	}

	stopped := -1
	var why *stop
	for i := range steps {
		if err := ctx.Err(); err != nil {
			why = r.interrupted(ctx, i)
			stopped = i
			break
		}
		res, st := r.step(ctx, i)
		res.Index = i
		if res.Action == "" {
			res.Action, _ = steps[i].Kind()
		}
		if steps[i].Secret || flows.Secure(steps[i]) {
			res = redactResult(res, steps[i])
		}
		r.res.Steps = append(r.res.Steps, res)
		if ctx.Err() != nil && (st != nil || !res.OK) {
			st = r.interrupted(ctx, i)
			r.res.Steps[len(r.res.Steps)-1].Error = st.err
		}
		if st != nil {
			why = st
			stopped = i
			break
		}
	}
	if why == nil {
		if st := r.openNotes(); st != nil {
			why = st
			stopped = len(steps) - 1
			if p := r.firstOpen(); p >= 0 {
				stopped = p
			}
		}
	}
	settled := false
	if why == nil && len(steps) > 0 {
		settled = r.endWait(ctx)
	}
	r.finish(ctx, stopped, why)
	r.res.Caveats = r.caveats()
	if u := tally.Snapshot(); len(u) > 0 {
		r.res.Usage = u
	}
	if chain != nil {
		front := r.endFront(ctx)
		done := len(steps)
		if stopped >= 0 {
			done = stopped
		}
		ended, err := chain.End(flows.Outcome{Done: done, OK: r.res.OK, Settled: r.res.OK && settled, Unsaved: r.unsaved, Front: front})
		if err == nil {
			r.res.Chain, r.res.SavedAs = ended.Chain, ended.SavedAs
		} else if e.Log != nil {
			e.Log("plan chain: %v", err)
		}
	}
	r.logRun()
	return r.res, nil
}

// turnOf is the call's desk turn; a call without one (tests, the CLI)
// passes freely.
func turnOf(call *engine.Call) func(context.Context) (func(), error) {
	if call == nil || call.Turn == nil {
		return func(context.Context) (func(), error) { return func() {}, nil }
	}
	return call.Turn
}

// interrupted is the stop of a plan whose timeout passed or whose call was
// cancelled, with the steps it finished.
func (r *planRun) interrupted(ctx context.Context, done int) *stop {
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &stop{err: &protocol.Error{
			Code:    protocol.ErrTimeout,
			Message: fmt.Sprintf("the plan hit its %d s timeout after %d of %d steps", int(r.timeout/time.Second), done, len(r.steps)),
			Hint:    "the finished steps may have taken effect: observe before running the rest",
		}}
	}
	return &stop{err: &protocol.Error{
		Code:    protocol.ErrStopped,
		Message: fmt.Sprintf("the plan was cancelled after %d of %d steps", done, len(r.steps)),
		Hint:    "the finished steps may have taken effect: observe before running the rest",
	}}
}

// finish fills the stop, the remaining steps, the view and the failure
// observe.
func (r *planRun) finish(ctx context.Context, stopped int, why *stop) {
	if why != nil {
		r.res.OK = false
		at := stopped
		r.res.StoppedAt = &at
		r.res.Error = scrubSecrets(why.err, r.steps)
		if stopped < len(r.raw) {
			r.res.Remaining = Remaining(r.raw[stopped:])
		}
		r.report = why.recorded
		if why.recorded && why.err != nil {
			s := r.raw[stopped]
			intent := s.Intent
			if flows.Secure(s) {
				intent = ""
			}
			report := fmt.Sprintf("stop report: step %d", stopped)
			if intent != "" {
				report += fmt.Sprintf(" (%s)", intent)
			}
			report += fmt.Sprintf(" stopped on its recorded check; %d steps remain from it", len(r.res.Remaining))
			if why.err.Hint != "" {
				report += "; " + why.err.Hint
			}
			why.err.Hint = report
		}
	}
	if r.e.X.Refs != nil {
		r.res.View = r.e.X.Refs.View()
	}
	if r.e.Observer != nil && (r.req.Observe || !r.res.OK) && !r.secretStop(stopped) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if ob := r.failureView(ctx); ob != nil {
			r.res.Observe = ob
			r.res.View = ob.View
			if r.res.Error != nil && r.res.Error.Observe == nil {
				r.res.Error.Observe = ob
			}
		}
	}
}

// secretStop says whether the plan stopped on a password step, whose view
// is never attached.
func (r *planRun) secretStop(stopped int) bool {
	return stopped >= 0 && stopped < len(r.steps) && (r.steps[stopped].Secret || flows.Secure(r.steps[stopped]))
}

func (r *planRun) endFront(ctx context.Context) flows.Window {
	listed, err := r.list(ctx)
	if err != nil {
		return flows.Window{}
	}
	if f := frontOf(listed); f != nil {
		return flows.Window{Title: f.Title, PID: f.PID, Dialog: dialogRole(f.Role)}
	}
	if r.win != nil {
		return flows.Window{Title: r.win.Title, PID: r.win.PID}
	}
	return flows.Window{}
}

func (r *planRun) logRun() {
	if r.e.Store == nil {
		return
	}
	entry := map[string]any{"at": r.e.now().UnixMilli(), "ok": r.res.OK, "steps": len(r.steps)}
	if r.opt.Flow != "" {
		entry["flow"] = r.opt.Flow
	}
	if r.res.StoppedAt != nil {
		entry["stopped_at"] = *r.res.StoppedAt
	}
	if r.res.Error != nil {
		entry["error"] = string(r.res.Error.Code)
	}
	var trace []map[string]any
	for _, s := range r.res.Steps {
		t := map[string]any{"i": s.Index, "action": s.Action, "status": s.Status, "ms": s.Ms}
		if s.How != "" {
			t["how"] = s.How
		}
		if len(s.Notes) > 0 && !r.steps[s.Index].Secret {
			t["notes"] = s.Notes
		}
		trace = append(trace, t)
	}
	entry["trace"] = trace
	if err := r.e.Store.LogRun(entry); err != nil && r.e.Log != nil {
		r.e.Log("plan run log: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Windows

// list reads the window list the policy admits.
func (r *planRun) list(ctx context.Context) ([]view.RawWindow, error) {
	raw, err := r.e.X.B.Windows(ctx)
	if err != nil {
		return nil, err
	}
	return r.e.policy().Allow.Filter(view.ListWindows(raw, r.e.policy().Input)), nil
}

func frontOf(ws []view.RawWindow) *view.RawWindow {
	for i := range ws {
		if ws[i].Active {
			w := ws[i]
			return &w
		}
	}
	return nil
}

func dialogRole(role string) bool {
	switch role {
	case "dialog", "sheet", "alert", "panel":
		return true
	}
	return false
}

// byKey finds a window by key.
func byKey(ws []view.RawWindow, key string) *view.RawWindow {
	for i := range ws {
		if ws[i].Key == key {
			w := ws[i]
			return &w
		}
	}
	return nil
}

// adoptStart takes the request's window as the plan window when it names
// one that exists.
func (r *planRun) adoptStart(listed []view.RawWindow) {
	if r.req.Window == "" {
		return
	}
	if strings.HasPrefix(r.req.Window, "w") && r.e.X.Refs != nil {
		if e, err := r.e.X.Refs.Lookup(r.req.Window); err == nil && e.Window {
			if w := byKey(listed, e.WindowKey); w != nil {
				r.win = w
			}
			return
		}
	}
	if w, err := view.PickWindow(listed, r.req.Window, 0); err == nil {
		r.win = &w
	}
}

// windowRef hands out the ref of the plan window, so steps address it by
// identity rather than by title.
func (r *planRun) windowRef(w view.RawWindow) string {
	if r.e.X.Refs == nil {
		r.e.X.Refs = view.NewRegistry(0)
	}
	walk := r.e.X.Refs.Begin()
	defer walk.End()
	return walk.Window(w)
}

// snapshot reads a window's tree and hands out refs for it.
func (r *planRun) snapshot(ctx context.Context, w view.RawWindow, opts view.TreeOptions) (view.Snapshot, error) {
	tree, err := r.e.X.B.Tree(ctx, w, opts)
	if err != nil {
		return view.Snapshot{}, err
	}
	if r.e.X.Refs == nil {
		r.e.X.Refs = view.NewRegistry(0)
	}
	walk := r.e.X.Refs.Begin()
	defer walk.End()
	return walk.Snapshot(tree), nil
}

// background says whether a step of this plan sends input from behind.
func (r *planRun) background(mode string) bool {
	return mode == protocol.InputBackground || (mode == protocol.InputAuto && !r.fg)
}

// mode is a step's input mode: the plan's, narrowed by the step; an auto
// plan that took the foreground keeps it.
func (r *planRun) mode(s protocol.Step) (string, *protocol.Error) {
	m, perr := policy.StepMode(r.input, s.Input)
	if perr != nil {
		return "", perr
	}
	if m == protocol.InputAuto && r.fg {
		return protocol.InputForeground, nil
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Results

func refused(perr *protocol.Error) protocol.StepResult {
	return protocol.StepResult{Status: protocol.StatusRefused, Error: perr}
}

func okResult(evidence string) protocol.StepResult {
	return protocol.StepResult{OK: true, Status: protocol.StatusConfirmed, Evidence: evidence}
}

// Remaining are steps as the agent may resend them: recorded crops and
// other recording-only payloads stripped, passwords and their intents
// redacted.
func Remaining(steps []protocol.Step) []protocol.Step {
	out := make([]protocol.Step, len(steps))
	for i, s := range steps {
		if flows.Secure(s) {
			if s.Type != nil && !strings.Contains(*s.Type, "{{") {
				hidden := "[secret]"
				s.Type = &hidden
			}
			if s.Set != nil && s.Value != nil && !strings.Contains(string(*s.Value), "{{") {
				hidden := json.RawMessage(`"[secret]"`)
				s.Value = &hidden
			}
			s.Intent = ""
		}
		if s.Recorded != nil {
			rec := *s.Recorded
			rec.Crop, rec.Offset, rec.Scale = "", nil, 0
			s.Recorded = &rec
		}
		out[i] = s
	}
	return out
}

// redactResult removes a password from everything a step result says.
func redactResult(res protocol.StepResult, s protocol.Step) protocol.StepResult {
	secret, ok := secretText(s)
	if !ok {
		return res
	}
	hide := func(v string) string {
		if strings.TrimSpace(secret) == "" {
			return v
		}
		return strings.ReplaceAll(v, secret, "the password")
	}
	res.Evidence = hide(res.Evidence)
	notes := make([]string, len(res.Notes))
	for i, n := range res.Notes {
		notes[i] = hide(n)
	}
	res.Notes = notes
	if res.Error != nil {
		e := *res.Error
		e.Message, e.Hint, e.Observe = hide(e.Message), hide(e.Hint), nil
		res.Error = &e
	}
	return res
}

func itoa(i int) string { return strconv.Itoa(i) }

func quote(s string) string { return strconv.Quote(s) }

// models is the model client, nil while none is ready: a client that
// reports Ready (built after the daemon listens) counts once it is.
func (e *Executor) models() Models {
	if e.Models == nil {
		return nil
	}
	if r, ok := e.Models.(interface{ Ready() bool }); ok && !r.Ready() {
		return nil
	}
	return e.Models
}

// scrubSecrets returns err with every password the plan's steps carry
// replaced, whatever produced the message: the plan's error is copied to
// the top of the result and must never repeat a secret.
func scrubSecrets(err *protocol.Error, steps []protocol.Step) *protocol.Error {
	if err == nil {
		return nil
	}
	e := err // in place: finish keeps adding to the same error
	for _, s := range steps {
		if !flows.Secure(s) {
			continue
		}
		secret, ok := secretText(s)
		if !ok || strings.TrimSpace(secret) == "" {
			continue
		}
		e.Message = strings.ReplaceAll(e.Message, secret, "the password")
		e.Hint = strings.ReplaceAll(e.Hint, secret, "the password")
	}
	return e
}

// dropLists forgets the window list shared with the actions executor, so
// the next listing reads the desktop.
func (e *Executor) dropLists() {
	if l := actions.ListsOf(e.X.B); l != nil {
		l.Drop()
	}
}
