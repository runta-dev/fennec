package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"fennec/cua/internal/protocol"
)

// Observer answers observe requests (view.Observer in production).
type Observer interface {
	Observe(ctx context.Context, req protocol.ObserveRequest) (*protocol.ObserveResult, error)
}

// Decode reads a tool's arguments strictly: unknown fields are refused, so a
// typo never silently changes what a call does.
func Decode(args json.RawMessage, into any) *protocol.Error {
	if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return &protocol.Error{Code: protocol.ErrInvalid, Message: "arguments: " + err.Error()}
	}
	return nil
}

// AsProtocol keeps a protocol error as it is and wraps anything else.
func AsProtocol(err error) *protocol.Error {
	if err == nil {
		return nil
	}
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe
	}
	return errorFor(err)
}

// Imager answers observe requests that ask for pixels (image or vision);
// vision.Service in production. base answers the rest of the request.
type Imager interface {
	Observe(ctx context.Context, agent string, req protocol.ObserveRequest, base Observer) (*protocol.ObserveResult, error)
}

// ObserveTool reads the desktop. It takes no desk turn: reads run alongside
// actions. Without an Imager, captures are refused.
func ObserveTool(o Observer) Tool { return ObserveToolWith(o, nil) }

// ObserveToolWith reads the desktop and hands requests for pixels to im.
func ObserveToolWith(o Observer, im Imager) Tool {
	return func(ctx context.Context, call *Call, args json.RawMessage) (any, *protocol.Error) {
		var req protocol.ObserveRequest
		if perr := Decode(args, &req); perr != nil {
			return nil, perr
		}
		var (
			res *protocol.ObserveResult
			err error
		)
		if req.Vision || (req.Image != "" && req.Image != "none") {
			if im == nil {
				return nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: "captures are not available in this build yet", Hint: "observe without image or vision"}
			}
			agent := ""
			if call != nil && call.Session != nil {
				agent = call.Session.AgentID
			}
			res, err = im.Observe(ctx, agent, req, o)
		} else {
			res, err = o.Observe(ctx, req)
		}
		if err != nil {
			return nil, AsProtocol(err)
		}
		return res, nil
	}
}
