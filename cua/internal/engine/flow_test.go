package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"fennec/cua/internal/flows"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/record"
	"fennec/cua/internal/view"
)

func flowCall(t *testing.T, tool Tool, args string) (*protocol.FlowResult, *protocol.Error) {
	t.Helper()
	out, perr := tool(context.Background(), &Call{}, json.RawMessage(args))
	res, _ := out.(*protocol.FlowResult)
	return res, perr
}

func TestFlowToolWithoutStoreOrRunner(t *testing.T) {
	tool := FlowTool(&Flows{})
	for _, args := range []string{`{"op":"list"}`, `{"op":"show","name":"x"}`, `{"op":"save","name":"x"}`} {
		if _, perr := flowCall(t, tool, args); perr == nil || perr.Code != protocol.ErrUnavailable {
			t.Errorf("%s without a store = %v", args, perr)
		}
	}
	if _, perr := flowCall(t, tool, `{"op":"record_start"}`); perr == nil || perr.Code != protocol.ErrUnavailable {
		t.Errorf("record without a recorder = %v", perr)
	}
	if _, perr := flowCall(t, tool, `{"op":"record_start","apps":"TextEdit"}`); perr == nil || perr.Code != protocol.ErrInvalid {
		t.Errorf("a non-list apps argument = %v", perr)
	}
	if _, perr := flowCall(t, tool, `{"op":"fly"}`); perr == nil || perr.Code != protocol.ErrInvalid {
		t.Errorf("an unknown op = %v", perr)
	}

	store, err := flows.NewStore(filepath.Join(t.TempDir(), "flows"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&flows.Saved{Name: "login", Params: map[string]protocol.FlowParam{"user": {Example: "alice"}, "password": {Secret: true}},
		Steps: []protocol.Step{{Launch: "Safari"}, {Type: ptr("{{user}}")}, {Type: ptr("{{password}}"), Secret: true}}}, "seed"); err != nil {
		t.Fatal(err)
	}
	f := &Flows{Store: store, Chains: &flows.Chains{Store: store}}
	tool = FlowTool(f)
	if _, perr := flowCall(t, tool, `{"op":"run","name":"login"}`); perr == nil || perr.Code != protocol.ErrInvalid || perr.Hint == "" {
		t.Errorf("a run without the password = %+v", perr)
	}
	if _, perr := flowCall(t, tool, `{"op":"run","name":"login","params":{"password":"pw"}}`); perr == nil || perr.Code != protocol.ErrUnavailable {
		t.Errorf("a run before the executor exists = %+v", perr)
	}
	var got protocol.ActRequest
	var opt PlanOptions
	f.Run = func(_ context.Context, _ *Call, req protocol.ActRequest, o PlanOptions) (*protocol.ActResult, *protocol.Error) {
		got, opt = req, o
		return &protocol.ActResult{OK: true, SavedAs: "login"}, nil
	}
	res, perr := flowCall(t, tool, `{"op":"run","name":"Login","params":{"password":"pw"}}`)
	if perr != nil || res.Run == nil || !res.Run.OK {
		t.Fatalf("run = %+v, %v", res, perr)
	}
	if opt.Flow != "login" || got.Params["user"] != "alice" || got.Params["password"] != "pw" || len(got.Steps) != 3 {
		t.Errorf("ran %+v with %+v", got, opt)
	}
	res, perr = flowCall(t, tool, `{"op":"list"}`)
	if perr != nil || len(res.Flows) != 1 || res.Flows[0].App != "Safari" {
		t.Errorf("list = %+v, %v", res, perr)
	}
}

type deskStub struct{}

func (deskStub) Windows(context.Context) ([]view.RawWindow, error) {
	return []view.RawWindow{{Key: "1", Title: "Untitled", Role: "window", PID: 7, App: "TextEdit", Active: true}}, nil
}
func (deskStub) At(context.Context, int, int) (*record.Hit, error)    { return nil, nil }
func (deskStub) Shot(context.Context, view.RawWindow) ([]byte, error) { return nil, nil }
func (deskStub) HoldWeb(context.Context, int) error                   { return nil }
func (deskStub) ReleaseWeb(context.Context)                           {}
func (deskStub) Bundle(int) string                                    { return "" }

func TestFlowToolRecordsADraft(t *testing.T) {
	var sink func(record.Input)
	rec := &record.Recorder{Dir: t.TempDir(), Desk: deskStub{}, Listen: func(s func(record.Input)) (func(), error) { sink = s; return func() {}, nil }}
	tool := FlowTool(&Flows{Recorder: rec})
	res, perr := flowCall(t, tool, `{"op":"record_start","apps":["TextEdit"]}`)
	if perr != nil || res.ID == "" {
		t.Fatalf("start = %+v, %v", res, perr)
	}
	for _, r := range "hi" {
		sink(record.Input{Kind: record.InputKey, Text: string(r)})
	}
	if _, perr := flowCall(t, tool, `{"op":"record_note","text":"the greeting varies"}`); perr != nil {
		t.Fatal(perr)
	}
	res, perr = flowCall(t, tool, `{"op":"record_stop","params":{"greeting":"hi"}}`)
	if perr != nil || res.Flow == nil {
		t.Fatalf("stop = %+v, %v", res, perr)
	}
	d := res.Flow
	if len(d.Steps) != 2 || d.Steps[0].Launch != "TextEdit" || *d.Steps[1].Type != "{{greeting}}" || d.Params["greeting"].Example != "hi" || len(d.Notes) != 1 || d.Recording == "" {
		t.Errorf("draft = %+v", d)
	}
}

func ptr(s string) *string { return &s }
