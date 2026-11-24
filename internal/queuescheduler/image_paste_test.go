package queuescheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/board"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestRequestImageUsesHerdrPutAndPasteWithoutAgentFlags(t *testing.T) {
	store := board.Store{Home: t.TempDir()}
	var pngBytes bytes.Buffer
	png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	attachment, err := store.SaveAttachment(pngBytes.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	request := board.Request{Session: "0123456789abcdef0123456789abcdef", Messages: []board.Message{{Attachments: []board.Attachment{attachment}}}}
	calls := []string{}
	query := func(_ context.Context, method string, params any) (json.RawMessage, error) {
		calls = append(calls, method)
		args := params.(map[string]any)
		if args["terminal"] != "t7" {
			t.Fatal("wrong terminal")
		}
		switch method {
		case "terminal.put":
			if !bytes.Equal(args["data"].([]byte), pngBytes.Bytes()) {
				t.Fatal("put changed image bytes")
			}
			return json.Marshal(map[string]any{"path": "/tmp/agent's images/shot.png", "bytes": attachment.Size})
		case "terminal.send":
			text := args["text"].(string)
			if !strings.HasPrefix(text, "'/tmp/agent'\\''s images/shot.png' ") || !strings.Contains(text, "Inspect the screenshot") || args["force"] != false {
				t.Fatal("paste or submit semantics changed")
			}
			return json.RawMessage(`{"started":true}`), nil
		}
		return nil, errors.New("unexpected core operation")
	}
	if err = PasteRequestImages(context.Background(), store, query, "t7", request, "Inspect the screenshot"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "terminal.put,terminal.send" {
		t.Fatal(calls)
	}
	if err = PasteRequestImages(context.Background(), store, query, "t7", request, "Inspect the screenshot"); err != nil || len(calls) != 2 {
		t.Fatal("replayed confirmed paste", err)
	}
}
func TestLostImageSubmitReplyNeverResubmits(t *testing.T) {
	store := board.Store{Home: t.TempDir()}
	var data bytes.Buffer
	png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	a, _ := store.SaveAttachment(data.Bytes())
	r := board.Request{Session: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Messages: []board.Message{{Attachments: []board.Attachment{a}}}}
	sends := 0
	query := func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		if method == "terminal.put" {
			return json.Marshal(map[string]any{"path": "/tmp/shot.png", "bytes": a.Size})
		}
		sends++
		return nil, errors.New("connection lost after send")
	}
	if PasteRequestImages(context.Background(), store, query, "t1", r, "question") == nil {
		t.Fatal("lost receipt accepted")
	}
	if PasteRequestImages(context.Background(), store, query, "t1", r, "question") == nil || sends != 1 {
		t.Fatal("ambiguous input was resent")
	}
}

func TestLocalImageRunDoesNotWaitForCloudBlobCopy(t *testing.T) {
	local := board.Store{Home: t.TempDir(), Role: "local"}
	cloud := board.Store{Home: t.TempDir(), Role: "cloud"}
	var data bytes.Buffer
	png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	attachment, err := local.SaveAttachment(data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := local.CreateProject(board.Project{Name: "Local images", Queue: 1})
	if _, err = local.CreateRequestWithAttachments(p.ID, "me", "Inspect", "", "", []board.Attachment{attachment}); err != nil {
		t.Fatal(err)
	}
	local.SetPrimary(true)
	local.SetRouting(board.Routing{Locations: map[string]string{"claude": "local"}})
	syncStores(t, local, cloud)
	if cloud.HasAttachment(attachment) {
		t.Fatal("fixture unexpectedly copied the blob")
	}
	cloudExecutor, localExecutor := &executorFixture{}, &executorFixture{}
	step(t, Scheduler{Store: cloud, Executor: cloudExecutor})
	syncStores(t, cloud, local)
	step(t, Scheduler{Store: local, Executor: localExecutor})
	if localExecutor.starts != 1 || cloudExecutor.starts != 0 {
		t.Fatal("local execution waited for the non-executing replica")
	}
}
