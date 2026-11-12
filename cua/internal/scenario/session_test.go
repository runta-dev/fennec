//go:build scenario

package scenario

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"fennec/cua/internal/mcpclient"
	"fennec/cua/internal/parity"
	"fennec/cua/internal/protocol"
)

// M is a JSON object.
type M = map[string]any

// Session is one side's MCP session, counting calls and keeping each
// result's text for the report.
type Session struct {
	c       *mcpclient.Client
	Backend string
	calls   int
	texts   []string
}

// Tool calls a tool and keeps its text.
func (s *Session) Tool(ctx context.Context, name string, args any) (mcpclient.ToolResult, error) {
	s.calls++
	r, err := s.c.Tool(ctx, name, args)
	text := r.Text()
	if err != nil {
		text = "error: " + err.Error()
	}
	in, _ := json.Marshal(args)
	s.texts = append(s.texts, clip(fmt.Sprintf("%s %s -> %s", name, in, text), 3000))
	return r, err
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ActOut is an act call's answer: a plan result, or an error when the plan
// could not start.
type ActOut struct {
	Res protocol.ActResult
	Err *protocol.Error // the call's own error, when it ran no plan
}

// OK says whether the plan passed.
func (a *ActOut) OK() bool { return a.Err == nil && a.Res.OK }

// Code is the failure code, empty on success.
func (a *ActOut) Code() string {
	switch {
	case a.Err != nil:
		return string(a.Err.Code)
	case a.Res.Error != nil:
		return string(a.Res.Error.Code)
	case !a.Res.OK:
		return "failed"
	}
	return ""
}

// Message is the failure message.
func (a *ActOut) Message() string {
	switch {
	case a.Err != nil:
		return a.Err.Message
	case a.Res.Error != nil:
		return a.Res.Error.Message
	}
	return ""
}

// Outcome converts to the protocol-neutral outcome.
func (a *ActOut) Outcome() parity.Outcome {
	return parity.Outcome{OK: a.OK(), Class: a.Code(), Detail: a.Message()}
}

// Step returns step i's result, or a zero result.
func (a *ActOut) Step(i int) protocol.StepResult {
	if i >= 0 && i < len(a.Res.Steps) {
		return a.Res.Steps[i]
	}
	return protocol.StepResult{}
}

// Act runs a fennec-cua act call.
func (s *Session) Act(ctx context.Context, req M) (*ActOut, error) {
	r, err := s.Tool(ctx, protocol.ToolAct, req)
	if err != nil {
		return nil, err
	}
	out := &ActOut{}
	text := r.Text()
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		return nil, fmt.Errorf("act answer is not JSON: %s", clip(text, 300))
	}
	if _, ok := probe["steps"]; ok {
		if err := json.Unmarshal([]byte(text), &out.Res); err != nil {
			return nil, err
		}
		return out, nil
	}
	var e struct {
		Error *protocol.Error `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &e); err != nil || e.Error == nil {
		return nil, fmt.Errorf("act answer has neither steps nor error: %s", clip(text, 300))
	}
	out.Err = e.Error
	return out, nil
}

// Steps runs act with steps and returns the outcome.
func (s *Session) Steps(ctx context.Context, steps ...M) (*ActOut, error) {
	return s.Act(ctx, M{"steps": steps})
}

// call runs a fennec-cua tool and decodes its result into out, or returns
// its error.
func (s *Session) call(ctx context.Context, tool string, args M, out any) (*protocol.Error, error) {
	r, err := s.Tool(ctx, tool, args)
	if err != nil {
		return nil, err
	}
	text := r.Text()
	if r.IsError {
		var e struct {
			Error *protocol.Error `json:"error"`
		}
		if json.Unmarshal([]byte(text), &e) == nil && e.Error != nil {
			return e.Error, nil
		}
		return &protocol.Error{Code: protocol.ErrInternal, Message: text}, nil
	}
	if out != nil {
		if err := json.Unmarshal([]byte(text), out); err != nil {
			return nil, fmt.Errorf("%s: %w: %s", tool, err, clip(text, 300))
		}
	}
	return nil, nil
}

// Observe runs a fennec-cua observe call.
func (s *Session) Observe(ctx context.Context, args M) (*protocol.ObserveResult, *protocol.Error, error) {
	var out protocol.ObserveResult
	perr, err := s.call(ctx, protocol.ToolObserve, args, &out)
	if err != nil || perr != nil {
		return nil, perr, err
	}
	return &out, nil, nil
}

// ObserveImage runs observe and also returns the first image block.
func (s *Session) ObserveImage(ctx context.Context, args M) (*protocol.ObserveResult, []byte, *protocol.Error, error) {
	r, err := s.Tool(ctx, protocol.ToolObserve, args)
	if err != nil {
		return nil, nil, nil, err
	}
	if r.IsError {
		var e struct {
			Error *protocol.Error `json:"error"`
		}
		_ = json.Unmarshal([]byte(r.Text()), &e)
		return nil, nil, e.Error, nil
	}
	var out protocol.ObserveResult
	if err := json.Unmarshal([]byte(r.Text()), &out); err != nil {
		return nil, nil, nil, err
	}
	img, err := firstImage(r)
	if img == nil && out.Image != nil && out.Image.Data != "" {
		// The image travels inside the answer.
		img, err = base64.StdEncoding.DecodeString(out.Image.Data)
	}
	return &out, img, nil, err
}

// Wait runs a fennec-cua wait call.
func (s *Session) Wait(ctx context.Context, args M) (*protocol.WaitResult, *protocol.Error, error) {
	var out protocol.WaitResult
	perr, err := s.call(ctx, protocol.ToolWait, args, &out)
	if err != nil || perr != nil {
		return nil, perr, err
	}
	return &out, nil, nil
}

// ---------------------------------------------------------------------------
// shuttle

// PlanOut is a shuttle gui_plan answer.
type PlanOut struct {
	OK       bool `json:"ok"`
	Done     int  `json:"done"`
	Trace    []M  `json:"trace"`
	Escalate *struct {
		Reason string `json:"reason"`
		Step   *int   `json:"step"`
		Window string `json:"window"`
	} `json:"escalate"`
	Error   string `json:"error"`
	Caveats []M    `json:"caveats"`
	IsError bool   `json:"-"`
	Raw     string `json:"-"`
}

// Reason is why the plan stopped.
func (p *PlanOut) Reason() string {
	switch {
	case p.Escalate != nil && p.Escalate.Reason != "":
		return p.Escalate.Reason
	case p.Error != "":
		return p.Error
	case !p.OK:
		return clip(p.Raw, 300)
	}
	return ""
}

// Outcome converts to the protocol-neutral outcome, classifying shuttle's
// free-text reason into fennec-cua's codes where the words allow.
func (p *PlanOut) Outcome() parity.Outcome {
	if p.OK {
		return parity.Outcome{OK: true}
	}
	r := p.Reason()
	return parity.Outcome{Class: classify(r), Detail: r}
}

// classify maps a shuttle failure text onto a fennec-cua error code.
func classify(r string) string {
	l := strings.ToLower(r)
	switch {
	case strings.Contains(l, "not on the app allowlist") || strings.Contains(l, "allowlist"):
		return string(protocol.ErrNotAllowed)
	case strings.Contains(l, "stopped by the user"):
		return string(protocol.ErrStopped)
	case strings.Contains(l, "another space"):
		return string(protocol.ErrOffSpace)
	case strings.Contains(l, "matches") && (strings.Contains(l, "windows") || strings.Contains(l, "two")):
		return string(protocol.ErrTargetAmbiguous)
	case strings.Contains(l, "focus"):
		return string(protocol.ErrFocusMismatch)
	case strings.Contains(l, "page") && (strings.Contains(l, "differ") || strings.Contains(l, "address")):
		return string(protocol.ErrPageMismatch)
	case strings.Contains(l, "covered") || strings.Contains(l, "covers"):
		return string(protocol.ErrCovered)
	case strings.Contains(l, "no window") || strings.Contains(l, "has no window"):
		return string(protocol.ErrWindowNotFound)
	case strings.Contains(l, "not found") || strings.Contains(l, "no match") || strings.Contains(l, "found no"):
		return string(protocol.ErrTargetNotFound)
	case strings.Contains(l, "still in front") || strings.Contains(l, "still open") || strings.Contains(l, "did not"):
		return string(protocol.ErrExpectFailed)
	case strings.Contains(l, "disabled"):
		return "disabled"
	}
	return "failed"
}

// Plan runs a shuttle gui_plan.
func (s *Session) Plan(ctx context.Context, steps ...M) (*PlanOut, error) {
	return s.PlanArgs(ctx, M{"steps": steps})
}

// PlanArgs runs gui_plan with full arguments.
func (s *Session) PlanArgs(ctx context.Context, args M) (*PlanOut, error) {
	r, err := s.Tool(ctx, "gui_plan", args)
	if err != nil {
		return nil, err
	}
	out := &PlanOut{IsError: r.IsError, Raw: r.Text()}
	text := r.Text()
	if i := strings.Index(text, "{"); i > 0 {
		// An error that says what it left: "...; what it left: {json}".
		text = text[i:]
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		out.OK = false
		out.Error = r.Text()
	}
	if r.IsError && out.Error == "" && out.Escalate == nil {
		out.OK = false
		out.Error = r.Text()
	}
	return out, nil
}

// DeskOut is a shuttle desk answer.
type DeskOut struct {
	IsError bool
	Text    string
	JSON    M
	Image   []byte
}

// Desk makes one shuttle desk call.
func (s *Session) Desk(ctx context.Context, verb string, args M) (*DeskOut, error) {
	call := M{"verb": verb}
	if args != nil {
		call["args"] = args
	}
	r, err := s.Tool(ctx, "desk", call)
	if err != nil {
		return nil, err
	}
	out := &DeskOut{IsError: r.IsError, Text: r.Text()}
	var v any
	if json.Unmarshal([]byte(out.Text), &v) == nil {
		if m, ok := v.(map[string]any); ok {
			out.JSON = m
		} else {
			out.JSON = M{"value": v}
		}
	}
	out.Image, _ = firstImage(r)
	return out, nil
}

// Outcome converts a desk answer.
func (d *DeskOut) Outcome() parity.Outcome {
	if !d.IsError {
		return parity.Outcome{OK: true}
	}
	return parity.Outcome{Class: classify(d.Text), Detail: clip(d.Text, 300)}
}

// fennecPlan is a driver that runs one act call.
func fennecPlan(steps ...M) Driver {
	return func(ctx context.Context, s *Session) (parity.Outcome, error) {
		a, err := s.Steps(ctx, steps...)
		if err != nil {
			return parity.Outcome{}, err
		}
		return a.Outcome(), nil
	}
}

// fennecAct is a driver that runs one act call with request fields.
func fennecAct(req M) Driver {
	return func(ctx context.Context, s *Session) (parity.Outcome, error) {
		a, err := s.Act(ctx, req)
		if err != nil {
			return parity.Outcome{}, err
		}
		return a.Outcome(), nil
	}
}

// shuttlePlan is a driver that runs one gui_plan.
func shuttlePlan(steps ...M) Driver {
	return func(ctx context.Context, s *Session) (parity.Outcome, error) {
		p, err := s.Plan(ctx, steps...)
		if err != nil {
			return parity.Outcome{}, err
		}
		return p.Outcome(), nil
	}
}

// shuttleDesk is a driver that makes one desk call.
func shuttleDesk(verb string, args M) Driver {
	return func(ctx context.Context, s *Session) (parity.Outcome, error) {
		d, err := s.Desk(ctx, verb, args)
		if err != nil {
			return parity.Outcome{}, err
		}
		return d.Outcome(), nil
	}
}

// chain runs drivers in order and answers the first failing outcome, or
// the last one.
func chain(ds ...Driver) Driver {
	return func(ctx context.Context, s *Session) (parity.Outcome, error) {
		var o parity.Outcome
		for _, d := range ds {
			var err error
			o, err = d(ctx, s)
			if err != nil || !o.OK {
				return o, err
			}
		}
		return o, nil
	}
}

// pause is a driver step that waits.
func pause(ms int) Driver {
	return func(ctx context.Context, s *Session) (parity.Outcome, error) {
		return parity.Outcome{OK: true}, sleep(ctx, ms)
	}
}
