package engine

import (
	"context"
	"encoding/json"
	"errors"

	"fennec/cua/internal/extensions"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
)

// owner is whom a session's desk calls pass as: a script plugin's own
// session (it presents its run token) passes as the caller that owns the run,
// so the run's steps re-enter the turn it holds.
func (e *Engine) owner(s *mcpserver.Session) string {
	if owner, ok := e.Ext.RunOwner(s.RunToken); ok {
		return owner
	}
	return s.AgentID
}

// callExtension answers a script plugin or service tool after the engine's
// stop and configuration checks; ok is false for any other tool.
func (e *Engine) callExtension(ctx context.Context, call *Call, name string, args json.RawMessage) (mcpserver.Result, bool) {
	switch {
	case e.Ext.IsPlugin(name):
		if e.RunOff {
			return result(nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: "scripts are turned off, and script plugins with them", Hint: "set run = true under [tools] in fennec-cua's config to turn them back on"}), true
		}
		out, perr := e.runPlugin(ctx, call, name, args)
		return result(out, perr), true
	case e.Ext.IsService(name):
		// A service call takes no desk turn; a stop ends it.
		reply, err := e.Ext.CallService(ctx, name, call.Session.AgentID, args)
		if err != nil {
			if ctx.Err() != nil {
				return result(nil, errorFor(sched.Cause(ctx))), true
			}
			code := protocol.ErrInternal
			if errors.Is(err, extensions.ErrNoService) {
				code = protocol.ErrUnavailable
			}
			return result(nil, &protocol.Error{Code: code, Message: err.Error()}), true
		}
		return extensions.Result(reply), true
	}
	return mcpserver.Result{}, false
}

// runPlugin runs a script plugin under the run tool's contract: it holds the
// desk turn until it ends and reports as run does.
func (e *Engine) runPlugin(ctx context.Context, call *Call, name string, args json.RawMessage) (any, *protocol.Error) {
	var req protocol.RunRequest
	if perr := Decode(args, &req); perr != nil {
		return nil, perr
	}
	release, err := call.Turn(ctx)
	if err != nil {
		return nil, TurnError(err)
	}
	defer release()
	report := e.Ext.RunPlugin(ctx, name, extensions.RunCall{Agent: e.owner(call.Session), Input: e.Input}, req)
	if report.Error == nil {
		return report, nil
	}
	code := protocol.ErrRunFailed
	switch report.Error.Kind {
	case protocol.RunTimeout:
		code = protocol.ErrTimeout
	case protocol.RunStopped:
		code = protocol.ErrStopped
	}
	return report, &protocol.Error{Code: code, Message: report.Error.Kind + ": " + report.Error.Message}
}

// Resources lists the script plugins' resources while scripts are on.
func (e *Engine) Resources(*mcpserver.Session) []mcpserver.Resource {
	if e.RunOff || e.Broken != nil {
		return nil
	}
	return e.Ext.Resources()
}

// ReadResource reads a script plugin's resource.
func (e *Engine) ReadResource(_ *mcpserver.Session, uri string) (mcpserver.ResourceContents, error) {
	if e.RunOff || e.Broken != nil {
		return mcpserver.ResourceContents{}, mcpserver.ErrUnknownResource
	}
	return e.Ext.ReadResource(uri)
}

var _ mcpserver.ResourceHandler = (*Engine)(nil)
