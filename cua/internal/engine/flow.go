package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"fennec/cua/internal/flows"
	"fennec/cua/internal/models"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/record"
)

// PlanOptions say why the flow tool runs a plan.
type PlanOptions struct {
	// Flow names the saved flow being replayed.
	Flow string
	// Save names the flow the plan is saved as when it passes.
	Save string
}

// PlanRunner runs an act plan for the flow tool; the plan executor
// provides it. It answers the plan's result even when the plan stopped,
// and an error alone only when the plan could not run at all.
type PlanRunner func(ctx context.Context, call *Call, req protocol.ActRequest, opt PlanOptions) (*protocol.ActResult, *protocol.Error)

// Flows is what the flow tool works with. A nil Store answers every
// store operation unavailable rather than guessing a directory.
type Flows struct {
	Store    *flows.Store
	Chains   *flows.Chains
	Recorder *record.Recorder
	// Caption names unnamed click targets in drafts; nil leaves them
	// named by their point.
	Caption flows.Captioner
	// Decider answers flow list queries.
	Decider flows.Decider
	// Run runs plans; until the plan executor sets it, flow run and saves
	// that must run answer unavailable.
	Run PlanRunner
}

// FlowTool answers the flow tool: saved flows and recordings.
func FlowTool(f *Flows) Tool {
	return func(ctx context.Context, call *Call, args json.RawMessage) (any, *protocol.Error) {
		var req protocol.FlowRequest
		if perr := Decode(args, &req); perr != nil {
			return nil, perr
		}
		switch req.Op {
		case protocol.FlowRecordStart, protocol.FlowRecordNote, protocol.FlowRecordStop:
			return f.recording(ctx, req)
		case protocol.FlowSave, protocol.FlowList, protocol.FlowShow, protocol.FlowRun, protocol.FlowDelete:
		default:
			return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "op " + quote(req.Op) + " is not save, list, show, run, delete, record_start, record_note or record_stop"}
		}
		if f.Store == nil {
			return nil, flowError(flows.ErrNoStore)
		}
		switch req.Op {
		case protocol.FlowList:
			return f.list(ctx, req)
		case protocol.FlowShow:
			if req.Name == "" {
				return nil, needName("show")
			}
			saved, err := f.Store.Load(req.Name)
			if err != nil {
				return nil, flowError(err)
			}
			return &protocol.FlowResult{Flow: saved.Flow()}, nil
		case protocol.FlowDelete:
			if req.Name == "" {
				return nil, needName("delete")
			}
			if err := f.Store.Delete(req.Name); err != nil {
				return nil, flowError(err)
			}
			return &protocol.FlowResult{}, nil
		case protocol.FlowSave:
			return f.save(ctx, call, req)
		}
		return f.replay(ctx, call, req)
	}
}

func (f *Flows) recording(ctx context.Context, req protocol.FlowRequest) (any, *protocol.Error) {
	if f.Recorder == nil {
		return nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: "recording is not available in this build"}
	}
	switch req.Op {
	case protocol.FlowRecordStart:
		id, err := f.Recorder.Start(req.Apps)
		if err != nil {
			return nil, flowError(err)
		}
		return &protocol.FlowResult{ID: id}, nil
	case protocol.FlowRecordNote:
		if strings.TrimSpace(req.Text) == "" {
			return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "record_note needs text"}
		}
		if err := f.Recorder.Note(req.Text); err != nil {
			return nil, flowError(err)
		}
		return &protocol.FlowResult{}, nil
	}
	rec, err := f.Recorder.Stop(ctx)
	if err != nil {
		return nil, flowError(err)
	}
	draft, err := flows.Draft(ctx, rec, req.Params, f.Caption)
	if err != nil {
		return nil, flowError(err)
	}
	return &protocol.FlowResult{Flow: draft}, nil
}

func (f *Flows) list(ctx context.Context, req protocol.FlowRequest) (any, *protocol.Error) {
	if strings.TrimSpace(req.Query) == "" {
		infos, err := f.Store.List()
		if err != nil {
			return nil, flowError(err)
		}
		return &protocol.FlowResult{Flows: infos}, nil
	}
	found, err := f.Store.Find(ctx, req.Query, f.Decider)
	if err != nil {
		return nil, flowError(err)
	}
	out := &protocol.FlowResult{Flows: []protocol.FlowInfo{}, Pick: &protocol.FlowPick{P: found.P}}
	if found.Flow != nil {
		out.Flows = []protocol.FlowInfo{found.Flow.Info()}
		out.Pick.Flow = found.Flow.Name
	}
	return out, nil
}

func (f *Flows) save(ctx context.Context, call *Call, req protocol.FlowRequest) (any, *protocol.Error) {
	if req.Name == "" {
		return nil, needName("save")
	}
	if f.Chains == nil {
		return nil, flowError(flows.ErrNoStore)
	}
	var run flows.Runner
	if f.Run != nil {
		run = func(ctx context.Context, steps []protocol.Step, params map[string]string, save string) (*protocol.ActResult, error) {
			res, perr := f.Run(ctx, call, protocol.ActRequest{Steps: steps, Params: params}, PlanOptions{Save: save})
			if res == nil && perr != nil {
				return nil, perr
			}
			return res, nil
		}
	}
	got, err := f.Chains.Save(ctx, req.Name, req.Steps, req.Params, req.From, run)
	if err != nil {
		return nil, flowError(err)
	}
	if got.Flow != nil && req.Description != "" {
		got.Flow.Description = req.Description
		got.Flow.SavedAt = ""
		if err := f.Store.Save(got.Flow, "described"); err != nil {
			return nil, flowError(err)
		}
	}
	ran := got.Ran
	out := &protocol.FlowResult{Run: got.Run, Ran: &ran}
	if got.Flow != nil {
		out.Flow = got.Flow.Flow()
	}
	if got.Run != nil && !got.Run.OK {
		perr := got.Run.Error
		if perr == nil {
			perr = &protocol.Error{Code: protocol.ErrExpectFailed, Message: "the plan stopped, so it was not saved"}
		}
		return out, perr
	}
	return out, nil
}

func (f *Flows) replay(ctx context.Context, call *Call, req protocol.FlowRequest) (any, *protocol.Error) {
	if req.Name == "" {
		return nil, needName("run")
	}
	saved, err := f.Store.Load(req.Name)
	if err != nil {
		return nil, flowError(err)
	}
	params := map[string]string{}
	for k, p := range saved.Params {
		if !p.Secret && p.Example != "" {
			params[k] = p.Example
		}
	}
	for k, v := range req.Params {
		params[k] = v
	}
	if name := flows.Missing(saved.Steps, params); name != "" {
		hint := "pass it in params"
		if saved.Params[name].Secret {
			hint = "it is a password the flow never stores: pass it in params"
		}
		return nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "no value for {{" + name + "}}; nothing ran", Hint: hint}
	}
	if f.Run == nil {
		return nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: "flow run is not available in this build yet"}
	}
	res, perr := f.Run(ctx, call, protocol.ActRequest{Steps: saved.Steps, Params: params}, PlanOptions{Flow: saved.Name})
	if res == nil {
		return nil, perr
	}
	out := &protocol.FlowResult{Run: res}
	if !res.OK {
		if perr == nil {
			perr = res.Error
		}
		if perr == nil {
			perr = &protocol.Error{Code: protocol.ErrExpectFailed, Message: "the flow stopped"}
		}
		return out, perr
	}
	return out, nil
}

func needName(op string) *protocol.Error {
	return &protocol.Error{Code: protocol.ErrInvalid, Message: "flow " + op + " needs a name"}
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

// flowError maps flow, recording and model failures onto protocol errors.
func flowError(err error) *protocol.Error {
	var pe *protocol.Error
	var me *models.Error
	switch {
	case errors.As(err, &pe):
		return pe
	case errors.As(err, &me):
		return me.Protocol()
	case errors.Is(err, flows.ErrNoStore):
		return &protocol.Error{Code: protocol.ErrUnavailable, Message: err.Error(), Hint: "configure the flow store; flows are read and saved only there"}
	case errors.Is(err, flows.ErrNothingToSave):
		return &protocol.Error{Code: protocol.ErrInvalid, Message: err.Error(), Hint: "pass steps to run and save, or run an act plan that starts with a launch step first"}
	case errors.Is(err, record.ErrStopped):
		return &protocol.Error{Code: protocol.ErrStopped, Message: err.Error()}
	case errors.Is(err, record.ErrNoTap):
		return &protocol.Error{Code: protocol.ErrUnavailable, Message: err.Error()}
	case errors.Is(err, record.ErrRunning), errors.Is(err, record.ErrNone), errors.Is(err, record.ErrNotLive):
		return &protocol.Error{Code: protocol.ErrInvalid, Message: err.Error()}
	case strings.Contains(err.Error(), "Input Monitoring"):
		return &protocol.Error{Code: protocol.ErrPermission, Message: err.Error()}
	case strings.HasPrefix(err.Error(), "no saved flow"):
		return &protocol.Error{Code: protocol.ErrInvalid, Message: err.Error(), Hint: "flow list names the saved flows"}
	}
	return AsProtocol(err)
}
