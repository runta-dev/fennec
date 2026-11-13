package engine

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
)

// Act call bounds.
const (
	DefaultActTimeout = 90 * time.Second
	MaxActTimeout     = 110 * time.Second
)

// ActTool answers act on the single-step path: it holds the desk turn for
// the whole request, asks in confirm mode once it holds it, and runs the
// steps one after another, stopping at the first that fails. Expectations,
// escalation, flows and chains belong to the plan executor (internal/plan),
// which replaces this loop; until then a step with expect alone, a wait step
// or a step's expect is answered unavailable rather than skipped.
func ActTool(x *actions.Executor, p *policy.Policy, o Observer) Tool {
	return func(ctx context.Context, call *Call, args json.RawMessage) (any, *protocol.Error) {
		var req protocol.ActRequest
		if perr := Decode(args, &req); perr != nil {
			return nil, perr
		}
		if len(req.Steps) == 0 {
			return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "act needs at least one step"}
		}
		for i, s := range req.Steps {
			if _, err := s.Kind(); err != nil {
				return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "step " + itoa(i) + ": " + err.Error()}
			}
			if s.Expect != nil || s.Wait != nil || s.Web != nil || s.Recorded != nil {
				return nil, &protocol.Error{
					Code:    protocol.ErrUnavailable,
					Message: "step " + itoa(i) + " uses expect, wait, web or recorded, which need the plan executor this build does not have yet; nothing was sent",
					Hint:    "send plain action steps and observe to check their effect",
				}
			}
		}
		timeout := DefaultActTimeout
		if req.TimeoutS > 0 {
			timeout = min(time.Duration(req.TimeoutS)*time.Second, MaxActTimeout)
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		var gate *policy.Gate
		if p != nil {
			gate = p.Gate
		}
		target := req.Window
		if target == "" {
			target = "the focused window"
		}
		release, err := gate.Turn(ctx, call.Turn, req.Steps, target)
		if err != nil {
			return nil, AsProtocol(err)
		}
		defer release()

		res := protocol.ActResult{OK: true}
		for i, s := range req.Steps {
			s = fill(s, req.Params)
			r := x.Step(ctx, actions.Request{Window: fillText(req.Window, req.Params), Input: req.Input}, s, i)
			res.Steps = append(res.Steps, r)
			if !r.OK {
				at := i
				res.OK, res.StoppedAt, res.Error = false, &at, r.Error
				res.Remaining = remaining(req.Steps[i:])
				break
			}
		}
		if x.Refs != nil {
			res.View = x.Refs.View()
		}
		if o != nil && (req.Observe || !res.OK) {
			if ob, err := o.Observe(ctx, protocol.ObserveRequest{Window: req.Window}); err == nil {
				res.Observe = ob
				res.View = ob.View
			}
		}
		return res, nil
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// remaining are the steps from the stopped one on, with secrets redacted
// and recorded crops stripped.
func remaining(steps []protocol.Step) []protocol.Step {
	out := make([]protocol.Step, len(steps))
	for i, s := range steps {
		if s.Secret && s.Type != nil {
			hidden := "[secret]"
			s.Type = &hidden
		}
		if s.Recorded != nil {
			r := *s.Recorded
			r.Crop = ""
			s.Recorded = &r
		}
		out[i] = s
	}
	return out
}

// fill replaces {{name}} placeholders in a step's text fields.
func fill(s protocol.Step, params map[string]string) protocol.Step {
	if len(params) == 0 {
		return s
	}
	if s.Type != nil {
		t := fillText(*s.Type, params)
		s.Type = &t
	}
	s.Key = fillText(s.Key, params)
	s.Menu = fillText(s.Menu, params)
	s.Focus = fillText(s.Focus, params)
	s.Close = fillText(s.Close, params)
	s.Launch = fillText(s.Launch, params)
	s.URL = fillText(s.URL, params)
	s.Window = fillText(s.Window, params)
	s.App = fillText(s.App, params)
	for _, t := range []**protocol.Target{&s.Press, &s.Set, &s.Click, &s.Scroll, &s.Drag, &s.To, &s.Into, &s.Perform, &s.SelectText} {
		if *t != nil {
			c := **t
			c.Select = fillText(c.Select, params)
			c.Window = fillText(c.Window, params)
			*t = &c
		}
	}
	if len(s.Args) > 0 {
		args := make([]string, len(s.Args))
		for i, a := range s.Args {
			args[i] = fillText(a, params)
		}
		s.Args = args
	}
	return s
}

func fillText(s string, params map[string]string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	for k, v := range params {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}
