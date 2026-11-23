package queuescheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/board"
	r "fennec/internal/runtime"
	"image"
	"image/png"
	"testing"
)

func TestCloudFollowUpReusesActiveTerminalAndFencesDelivery(t *testing.T) {
	store := board.Store{Home: t.TempDir()}
	p, _ := store.CreateProject(board.Project{Name: "Thread", Queue: 1})
	request, _ := store.CreateRequest(p.ID, "me", "Question", "", "")
	store.ClaimAt(p.ID, request.Number, "calm-fox", "claude", "cloud")
	store.Complete(p.ID, request.Number, "calm-fox", "claude", "Need more details", board.NeedsYou)
	store.FollowUp(p.ID, request.Number, "me", "Details", nil)
	request, err := store.ClaimAt(p.ID, request.Number, "green-leaf", "claude", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	sends := 0
	backend := &r.LocalRuntime{Home: store.Home, Run: func(context.Context, ...string) ([]byte, error) { t.Fatal("launched a new session"); return nil, nil }}
	backend.Query = func(_ context.Context, method string, args any) (json.RawMessage, error) {
		switch method {
		case "terminal.list":
			return json.RawMessage(`{"terminals":[{"address":"t1","state":"live","labels":{"fennec.session":"calm-fox","fennec.agent":"claude"}}]}`), nil
		case "terminal.label":
			return json.RawMessage(`{}`), nil
		case "terminal.send":
			sends++
			if args.(map[string]any)["terminal"] != "t1" {
				t.Fatal(args)
			}
			return json.RawMessage(`{}`), nil
		}
		t.Fatal(method)
		return nil, nil
	}
	executor := CloudExecutor{Runtime: backend, Store: store}
	for i := 0; i < 2; i++ {
		if err = executor.Start(context.Background(), p, request, "Follow up"); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := store.Request(p.ID, request.Number)
	if sends != 1 || current.ExecutionSession() != "calm-fox" || current.Session != "green-leaf" {
		t.Fatal(sends, current)
	}
}
func TestLostTextReceiptStaysUnknownEvenWhenTerminalIsLive(t *testing.T) {
	store := board.Store{Home: t.TempDir()}
	p, _ := store.CreateProject(board.Project{Name: "Text", Queue: 1})
	req, _ := store.CreateRequest(p.ID, "me", "Question", "", "")
	req, _ = store.Claim(p.ID, req.Number, "calm-fox", "claude")
	sends := 0
	query := func(context.Context, string, any) (json.RawMessage, error) {
		sends++
		return nil, errors.New("lost reply")
	}
	for i := 0; i < 2; i++ {
		err := SendRequest(context.Background(), store, query, "t1", req, "question")
		if !errors.Is(err, ErrInputUnconfirmed) {
			t.Fatal(err)
		}
		if err = (Scheduler{Store: store}).startFailed(p.ID, req.Number, req.Session, err); i == 0 && err != nil {
			t.Fatal(err)
		}
	}
	if sends != 1 {
		t.Fatal("replayed input", sends)
	}
	if _, err := store.Reconcile(p.ID, req.Number, req.Session, "live"); !errors.Is(err, board.ErrConflict) {
		t.Fatal("live process falsely proved input delivery", err)
	}
}

func TestRetryAfterRejectedLaunchRetainsImages(t *testing.T) {
	store := board.Store{Home: t.TempDir()}
	var data bytes.Buffer
	png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	attachment, err := store.SaveAttachment(data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	request := board.Request{Session: "calm-fox", Delivered: 1, Messages: []board.Message{{ID: 1, UID: "picture", Attachments: []board.Attachment{attachment}}}, Runs: []board.Run{{State: "not_started", InputUIDs: []string{"picture"}}, {State: "starting", InputAfter: 1}}}
	puts := 0
	query := func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		if method == "terminal.put" {
			puts++
			return json.Marshal(map[string]any{"path": "/tmp/image.png", "bytes": attachment.Size})
		}
		return json.RawMessage(`{"started":true}`), nil
	}
	if err = SendRequest(context.Background(), store, query, "t1", request, "retry"); err != nil {
		t.Fatal(err)
	}
	if puts != 1 {
		t.Fatal("retry dropped original image")
	}
}
