// Package engine answers fennec-cua's six MCP tools. It applies the safety
// controls every call passes (stop, then the desk turn for input) and hands
// each tool to its implementation.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"

	"fennec/cua/internal/extensions"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
	"fennec/cua/internal/schema"
)

// Version is set at build time.
var Version = "0.0.0-dev"

// Platform answers the parts of status that depend on the machine.
type Platform interface {
	AccessibilityTrusted() bool
	ScreenCaptureAllowed() bool
	MainDisplayPixels() (w, h int, ok bool)
}

// Tool runs one tool's request after the engine's checks.
type Tool func(ctx context.Context, call *Call, args json.RawMessage) (any, *protocol.Error)

// Call is what a tool knows about its caller.
type Call struct {
	Session *mcpserver.Session
	// Turn takes the desk turn for this caller; tools that send input call it.
	Turn func(ctx context.Context) (release func(), err error)

	// acquire takes the turn as a given owner (a run's own id); nil for
	// calls made outside Engine.Call.
	acquire func(ctx context.Context, owner string) (func(), error)
	// holds collects what a run holds on the desk; nil outside a run.
	holds *holds
	runID string
}

// Engine implements mcpserver.Handler.
type Engine struct {
	Turn     *sched.Turn
	Stopper  *sched.Stopper
	Platform Platform
	Input    string // configured input mode
	// Impl holds tool implementations; a tool without one answers
	// unavailable.
	Impl map[string]Tool
	// Broken is the configuration's load error: while set, every tool
	// but status answers it (stop and resume work through the control
	// socket regardless).
	Broken error
	// RunOff turns the run tool and script plugins off by configuration.
	RunOff bool
	// Ext holds the script and service plugins; nil means none.
	Ext *extensions.Extensions
	// Admit is the input policy's check before every tool but status, such
	// as refusing calls while a confirm dialog is open; nil admits all.
	Admit func(tool string) *protocol.Error

	// runs names each run; its nonce is drawn once per engine, that is
	// once per daemon.
	runs sched.RunIDs
}

var descriptions = map[string]string{
	protocol.ToolObserve: "See the desktop: the front window's outline with element refs (default), the window list (scope desktop), one element's value (scope element), what is at a point (scope point), a semantic find, or a capture (image). vision: true returns a screenshot with numbered elements.",
	protocol.ToolAct:     "Do one or more steps (press, set, click, type, key, scroll, drag, menu, focus, close, launch, wait, or expect alone), each optionally with expect. Stops at the first surprise and answers the trace, what changed and the current view. Outcomes: refused, declined and busy (another agent holds the desktop, or a confirm dialog is pending) sent nothing and are safe to retry; stopped (the user stopped fennec-cua), unconfirmed (sent but not proven), a type step's error after lines it reports as typed, and a stopped plan's partial trace may have taken effect, so observe before resending. In a macOS Qt app with no accessibility focus, background typing into a search field may land in another field: scroll the list instead, or use input foreground.",
	protocol.ToolWait:    "Wait until a window appears or goes, an element appears, a value matches, the window list changes, or the screen is still.",
	protocol.ToolRun:     "Run a TypeScript or JavaScript module whose main(params) uses the cua API; answers its result, logs and model usage. A run that fails or is stopped may already have acted: its report lists what it did.",
	protocol.ToolFlow:    "Saved flows (save, list with query, show, run, delete) and recordings of the user's input (record_start, record_note, record_stop) that become a draft plan.",
	protocol.ToolStatus:  "Version, permissions, input mode, whether the user stopped fennec-cua, and the policy in force.",
}

var requests = map[string]any{
	protocol.ToolObserve: protocol.ObserveRequest{},
	protocol.ToolAct:     protocol.ActRequest{},
	protocol.ToolWait:    protocol.WaitRequest{},
	protocol.ToolRun:     protocol.RunRequest{},
	protocol.ToolFlow:    protocol.FlowRequest{},
	protocol.ToolStatus:  struct{}{},
}

// Tools lists the six tools, then the extensions' tools.
func (e *Engine) Tools(*mcpserver.Session) []mcpserver.Tool {
	out := make([]mcpserver.Tool, 0, len(protocol.Tools))
	for _, name := range protocol.Tools {
		out = append(out, mcpserver.Tool{Name: name, Description: descriptions[name], InputSchema: schema.For(requests[name])})
	}
	return append(out, e.Ext.Tools(!e.RunOff)...)
}

// Call answers a tool call.
func (e *Engine) Call(ctx context.Context, s *mcpserver.Session, name string, args json.RawMessage) mcpserver.Result {
	if name == protocol.ToolStatus {
		return result(e.Status(), nil)
	}
	if e.Broken != nil {
		return result(nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: "the configuration does not load: " + e.Broken.Error(), Hint: "fix the file; fennec-cua stays closed until it loads"})
	}
	if e.Admit != nil {
		if perr := e.Admit(name); perr != nil {
			return result(nil, perr)
		}
	}
	gctx, guard, done, err := e.Stopper.Guard(ctx)
	if err != nil {
		return result(nil, &protocol.Error{Code: protocol.ErrStopped, Message: err.Error()})
	}
	defer done()
	if name == protocol.ToolRun && e.RunOff {
		return result(nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: "scripts are turned off", Hint: "set run = true under [tools] in fennec-cua's config to turn them back on"})
	}
	acquire := func(ctx context.Context, owner string) (func(), error) {
		release, err := e.Turn.Acquire(ctx, owner)
		if err != nil {
			if ctx.Err() != nil {
				// A stop while waiting answers stopped, not a bare cancel.
				return nil, sched.Cause(ctx)
			}
			return nil, err
		}
		if err := guard.Valid(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	}
	call := &Call{Session: s, acquire: acquire, Turn: func(ctx context.Context) (func(), error) {
		return acquire(ctx, e.owner(s))
	}}
	if res, ok := e.callExtension(gctx, call, name, args); ok {
		return res
	}
	impl := e.Impl[name]
	if impl == nil {
		return result(nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: name + " is not available in this build yet"})
	}
	out, perr := impl(gctx, call, args)
	if perr == nil && gctx.Err() != nil {
		perr = errorFor(sched.Cause(gctx))
	}
	return result(out, perr)
}

// errorFor maps scheduling failures onto protocol errors.
func errorFor(err error) *protocol.Error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sched.ErrStopped):
		return &protocol.Error{Code: protocol.ErrStopped, Message: err.Error()}
	case errors.Is(err, sched.ErrBusy):
		return &protocol.Error{Code: protocol.ErrBusy, Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		return &protocol.Error{Code: protocol.ErrTimeout, Message: "timed out"}
	}
	return &protocol.Error{Code: protocol.ErrInternal, Message: err.Error()}
}

// TurnError maps an Acquire failure for tools.
func TurnError(err error) *protocol.Error { return errorFor(err) }

func result(v any, perr *protocol.Error) mcpserver.Result {
	if perr != nil && v != nil {
		// A failure with a report (a run) answers the whole report, so
		// its logs travel with the error.
		out := map[string]any{"error": perr, "report": v}
		data, _ := json.Marshal(out)
		return mcpserver.Result{Text: string(data), IsError: true, Structured: out}
	}
	if perr != nil {
		data, _ := json.Marshal(map[string]any{"error": perr})
		return mcpserver.Result{Text: string(data), IsError: true, Structured: map[string]any{"error": perr}}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return result(nil, &protocol.Error{Code: protocol.ErrInternal, Message: err.Error()})
	}
	return mcpserver.Result{Text: string(data), Structured: v}
}

// Status describes the host.
func (e *Engine) Status() any {
	st := protocol.StatusResult{
		Version: Version,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Input:   e.Input,
		Stopped: e.Stopper.Stopped(),
		Permissions: map[string]string{
			"accessibility":    "unknown",
			"screen_recording": "unknown",
		},
		Run: e.Impl[protocol.ToolRun] != nil && !e.RunOff,
	}
	if st.Input == "" {
		st.Input = protocol.InputForeground
	}
	if p := e.Platform; p != nil {
		st.Permissions["accessibility"] = grant(p.AccessibilityTrusted())
		st.Permissions["screen_recording"] = grant(p.ScreenCaptureAllowed())
		if w, h, ok := p.MainDisplayPixels(); ok {
			st.Screen = [2]int{w, h}
		}
	}
	return st
}

func grant(ok bool) string {
	if ok {
		return "granted"
	}
	return "denied"
}

// Instructions is the initialize text agents read first.
var Instructions = strings.TrimSpace(`
fennec-cua drives this Mac's desktop. Start with observe: it answers the front window's outline, where each element has a ref such as e12. Then act with steps that name targets by ref, by selector (button "Save", textbox "Name" in sheet "Export") or, last, by point. Every act step reports confirmed, unconfirmed (sent but not proven: read back before resending) or refused (nothing sent). Add expect to a step to have it checked. Refused, declined and busy answers (another agent acting, or a confirm dialog pending) sent nothing: retrying is safe. Stopped, unconfirmed, a type error after the lines it reports as typed, and a plan.s partial trace may have taken effect: observe before resending. If a call answers stopped, the user halted fennec-cua: wait for them to resume.
`)
