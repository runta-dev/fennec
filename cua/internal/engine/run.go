package engine

import (
	"context"
	"encoding/json"
	"sync"

	"fennec/cua/internal/protocol"
	"fennec/cua/internal/script"
)

// Models answers a script's model calls; usage is reported per run.
type Models interface {
	// Call runs ask, decide or ground and returns its result and the usage
	// of that one call.
	Call(ctx context.Context, op string, args json.RawMessage) (any, protocol.Usage, *protocol.Error)
}

// RunTool runs scripts. The run holds the desk turn until it ends, so two
// runs never interleave their actions, while the run's own steps pass as the
// same caller.
func (e *Engine) RunTool(models Models) Tool {
	return func(ctx context.Context, call *Call, args json.RawMessage) (any, *protocol.Error) {
		var req protocol.RunRequest
		if perr := Decode(args, &req); perr != nil {
			return nil, perr
		}
		release, err := call.Turn(ctx)
		if err != nil {
			return nil, TurnError(err)
		}
		defer release()
		host := &runHost{e: e, call: call, models: models}
		report := script.Run(ctx, host, req)
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
	}
}

type runHost struct {
	e      *Engine
	call   *Call
	models Models

	mu    sync.Mutex
	usage protocol.Usage
}

func (h *runHost) Tool(ctx context.Context, name string, args json.RawMessage) (any, *protocol.Error) {
	if name == protocol.ToolStatus {
		return h.e.Status(), nil
	}
	if name == protocol.ToolRun {
		return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "a run cannot start another run"}
	}
	impl := h.e.Impl[name]
	if impl == nil {
		return nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: name + " is not available in this build yet"}
	}
	return impl(ctx, h.call, args)
}

func (h *runHost) Model(ctx context.Context, op string, args json.RawMessage) (any, *protocol.Error) {
	if h.models == nil {
		return nil, &protocol.Error{Code: protocol.ErrModelUnavailable, Message: "no model endpoint is configured"}
	}
	out, usage, perr := h.models.Call(ctx, op, args)
	h.mu.Lock()
	if h.usage == nil {
		h.usage = protocol.Usage{}
	}
	for m, u := range usage {
		t := h.usage[m]
		t.Calls += u.Calls
		t.InputTokens += u.InputTokens
		t.OutputTokens += u.OutputTokens
		t.CachedTokens += u.CachedTokens
		h.usage[m] = t
	}
	h.mu.Unlock()
	return out, perr
}

func (h *runHost) Usage() protocol.Usage {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := protocol.Usage{}
	for k, v := range h.usage {
		out[k] = v
	}
	return out
}

func (h *runHost) Input() string { return h.e.Input }
