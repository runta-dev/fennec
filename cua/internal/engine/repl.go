package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
	"fennec/cua/internal/schema"
	"fennec/cua/internal/script"
)

// MaxREPLs bounds the session runtimes kept at once; the least recently
// used one is discarded beyond it.
const MaxREPLs = 64

// CompletionAck answers a js call that carries only a completion summary.
const CompletionAck = "Completion summary received."

var replDescriptions = map[string]string{
	protocol.ToolJS:      "Run JavaScript in a persistent REPL that drives this Mac's apps through the `cua` object (top-level await; bindings persist across calls). The first call returns the API documentation. Pass `title`, a short present-progressive label of what the call does.",
	protocol.ToolJSReset: "Discard the REPL runtime and all of its bindings; apps and windows are left as they are. The next js call shows the API documentation again.",
}

// replSlot is one session's runtime.
type replSlot struct {
	repl *script.REPL
	used time.Time
}

type repls struct {
	mu sync.Mutex
	by map[*mcpserver.Session]*replSlot
}

func (r *repls) get(s *mcpserver.Session, timeout time.Duration) *script.REPL {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.by == nil {
		r.by = map[*mcpserver.Session]*replSlot{}
	}
	now := time.Now()
	if sl := r.by[s]; sl != nil {
		sl.used = now
		return sl.repl
	}
	if len(r.by) >= MaxREPLs {
		var oldest *mcpserver.Session
		for k, sl := range r.by {
			if oldest == nil || sl.used.Before(r.by[oldest].used) {
				oldest = k
			}
		}
		r.by[oldest].repl.Close()
		delete(r.by, oldest)
	}
	rp := script.NewREPL()
	rp.Timeout = timeout
	r.by[s] = &replSlot{repl: rp, used: now}
	return rp
}

// drop discards a session's runtime, ending its call in flight.
func (r *repls) drop(s *mcpserver.Session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	sl := r.by[s]
	if sl == nil {
		return false
	}
	sl.repl.Close()
	delete(r.by, s)
	return true
}

// CloseSession discards the session's runtime when its connection ends.
func (e *Engine) CloseSession(s *mcpserver.Session) { e.repl.drop(s) }

// HiddenTool names tools that are callable over MCP but never listed:
// turn_ended for every session, and the engine and extension tools only for
// a script plugin's own session, which presents its run's token and calls
// back as that run. Every other session reaches those tools through the
// REPL alone; a call by name answers unknown tool.
func (e *Engine) HiddenTool(s *mcpserver.Session, name string) bool {
	if name == protocol.ToolTurnEnded {
		return true
	}
	if s == nil || s.RunToken == "" {
		return false
	}
	if _, ok := e.Ext.RunOwner(s.RunToken); !ok {
		return false
	}
	for _, t := range protocol.Tools {
		if t == name {
			return true
		}
	}
	return (e.Ext.IsPlugin(name) && !e.RunOff) || e.Ext.IsService(name)
}

var _ mcpserver.SessionCloser = (*Engine)(nil)

// replTools lists js and js_reset.
func replTools() []mcpserver.Tool {
	return []mcpserver.Tool{
		{Name: protocol.ToolJS, Description: replDescriptions[protocol.ToolJS], InputSchema: schema.For(protocol.JSRequest{})},
		{Name: protocol.ToolJSReset, Description: replDescriptions[protocol.ToolJSReset], InputSchema: schema.For(struct{}{})},
	}
}

// replCall answers js, js_reset and turn_ended; ok is false for any other
// tool.
func (e *Engine) replCall(ctx context.Context, s *mcpserver.Session, name string, args json.RawMessage) (mcpserver.Result, bool) {
	switch name {
	case protocol.ToolJSReset:
		e.repl.drop(s)
		return mcpserver.Result{Text: "The REPL runtime was reset; the next js call starts a fresh one."}, true
	case protocol.ToolTurnEnded:
		return mcpserver.Result{Text: "ok"}, true
	case protocol.ToolJS:
	default:
		return mcpserver.Result{}, false
	}
	var req protocol.JSRequest
	if perr := Decode(args, &req); perr != nil {
		return replError(0, perr.Message), true
	}
	if strings.TrimSpace(req.Code) == "" {
		if strings.TrimSpace(req.CompletionSummary) != "" {
			return mcpserver.Result{Text: CompletionAck}, true
		}
		return replError(0, "js needs code"), true
	}
	if e.Broken != nil {
		return replError(0, "the configuration does not load: "+e.Broken.Error()+"; fix the file, fennec-cua stays closed until it loads"), true
	}
	if e.Admit != nil {
		if perr := e.Admit(name); perr != nil {
			return replError(0, perr.Message), true
		}
	}
	gctx, guard, done, err := e.Stopper.Guard(ctx)
	if err != nil {
		return replError(0, "stopped: "+err.Error()), true
	}
	defer done()
	acquire := func(ctx context.Context, owner string) (func(), error) {
		release, err := e.Turn.Acquire(ctx, owner)
		if err != nil {
			if ctx.Err() != nil {
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
	call := &Call{Session: s, acquire: acquire, Turn: func(ctx context.Context) (func(), error) { return acquire(ctx, e.owner(s)) }}
	// The call takes the desk turn, under its own id, at its first input
	// and keeps it until it ends: its later steps re-enter it and no other
	// agent's input interleaves with them. A call that only reads never
	// takes it.
	rc, turn := e.runCall(call)
	var (
		mu   sync.Mutex
		held func()
	)
	rc.Turn = func(ctx context.Context) (func(), error) {
		mu.Lock()
		defer mu.Unlock()
		if held == nil {
			r, err := turn(ctx)
			if err != nil {
				return nil, err
			}
			held = r
		}
		return turn(ctx)
	}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		if held != nil {
			held()
		}
	}()
	defer rc.holds.releaseAll()
	rp := e.repl.get(s, e.JSTimeout)
	host := &replHost{runHost: runHost{e: e, call: rc, models: e.Models}}
	res := rp.Eval(gctx, host, req.Code)
	return replResult(res), true
}

// replResult frames a call: the wall time, the documentation on a fresh
// runtime, the output parts, then the error.
func replResult(res script.EvalResult) mcpserver.Result {
	out := mcpserver.Result{IsError: res.Err != ""}
	out.Parts = append(out.Parts, mcpserver.Part{Text: framing(res.Wall)})
	if res.Fresh {
		out.Parts = append(out.Parts, mcpserver.Part{Text: script.Guide()})
	}
	for _, p := range res.Parts {
		if p.Image != nil {
			out.Parts = append(out.Parts, mcpserver.Part{Image: &mcpserver.Image{Data: p.Image.Data, MIMEType: p.Image.MIME}})
			continue
		}
		out.Parts = append(out.Parts, mcpserver.Part{Text: p.Text})
	}
	if res.Err != "" {
		out.Parts = append(out.Parts, mcpserver.Part{Text: res.Err})
	}
	return out
}

func framing(d time.Duration) string {
	return fmt.Sprintf("Wall time: %.4f seconds\nOutput:", d.Seconds())
}

func replError(d time.Duration, msg string) mcpserver.Result {
	if !strings.HasPrefix(msg, "Error") {
		msg = "Error: " + msg
	}
	return mcpserver.Result{IsError: true, Parts: []mcpserver.Part{{Text: framing(d)}, {Text: msg}}}
}

// replHost is a js call's host: the engine's tools as the call's own
// caller, plus scripts, extensions and status.
type replHost struct {
	runHost
}

func (h *replHost) Tool(ctx context.Context, name string, args json.RawMessage) (any, *protocol.Error) {
	e := h.e
	switch name {
	case protocol.ToolStatus:
		return e.Status(), nil
	case protocol.ToolRun:
		if e.RunOff {
			return nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: "scripts are turned off", Hint: "set run = true under [tools] in fennec-cua's config to turn them back on"}
		}
		var req protocol.RunRequest
		if perr := Decode(args, &req); perr != nil {
			return nil, perr
		}
		// A run owns the desk until it ends, as the run tool does.
		release, err := h.call.Turn(ctx)
		if err != nil {
			return nil, TurnError(err)
		}
		defer release()
		report := script.Run(ctx, &runHost{e: e, call: h.call, models: h.models}, req)
		if report.Error != nil {
			code := protocol.ErrRunFailed
			switch report.Error.Kind {
			case protocol.RunTimeout:
				code = protocol.ErrTimeout
			case protocol.RunStopped:
				code = protocol.ErrStopped
			}
			return report, &protocol.Error{Code: code, Message: report.Error.Kind + ": " + report.Error.Message}
		}
		return report, nil
	case "extensions":
		type tool struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		}
		out := []tool{}
		for _, t := range e.Ext.Tools(!e.RunOff) {
			out = append(out, tool{t.Name, t.Description, t.InputSchema})
		}
		return out, nil
	case protocol.ToolJS, protocol.ToolJSReset, protocol.ToolTurnEnded:
		return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: name + " is the REPL itself, not a tool it calls"}
	}
	if impl := e.Impl[name]; impl != nil {
		return impl(ctx, h.call, args)
	}
	if res, ok := e.callExtension(ctx, h.call, name, args); ok {
		return extensionValue(res)
	}
	return nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: name + " is not available in this build"}
}

// extensionValue turns an extension tool's MCP result into a value.
func extensionValue(res mcpserver.Result) (any, *protocol.Error) {
	text := res.Text
	if text == "" {
		for _, p := range res.Parts {
			text += p.Text
		}
	}
	if res.IsError {
		var wrapped struct {
			Error *protocol.Error `json:"error"`
		}
		if json.Unmarshal([]byte(text), &wrapped) == nil && wrapped.Error != nil {
			return nil, wrapped.Error
		}
		return nil, &protocol.Error{Code: protocol.ErrInternal, Message: text}
	}
	if res.Structured != nil {
		return res.Structured, nil
	}
	var v any
	if json.Unmarshal([]byte(text), &v) == nil {
		return v, nil
	}
	return text, nil
}
