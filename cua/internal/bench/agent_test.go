package bench

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fennec/cua/internal/mcpclient"
)

// A fake model that calls one tool, then finishes; a fake MCP backend over
// pipes that answers it with text and an image.
func TestRunCallsToolsThenFinishes(t *testing.T) {
	var turn atomic.Int32
	var sawImage atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "data:image/png;base64,AAAA") {
			sawImage.Store(true)
		}
		if turn.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"looking","tool_calls":[{"id":"c1","type":"function","function":{"name":"observe","arguments":"{\"scope\":\"desktop\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"DONE: it is there"}}],"usage":{"prompt_tokens":20,"completion_tokens":3}}`))
	}))
	defer srv.Close()

	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	go fakeBackend(t, sr, sw)
	c := mcpclient.New(cw, cr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tr := Run(ctx, Model{BaseURL: srv.URL, Name: "m"}, c, "find the window", AgentOptions{})
	if tr.Stop != "done" || tr.ToolCalls != 1 || !strings.HasPrefix(tr.Final, "DONE") || tr.InputTokens != 30 {
		t.Fatalf("%+v", tr)
	}
	if !sawImage.Load() {
		t.Error("the tool's image never reached the model")
	}
}

func fakeBackend(t *testing.T, r io.Reader, w io.Writer) {
	dec := json.NewDecoder(r)
	for {
		var req struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		if err := dec.Decode(&req); err != nil {
			return
		}
		if req.ID == nil {
			continue
		}
		var result any = map[string]any{}
		switch req.Method {
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "observe", "description": "see", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "Finder window"}, {"type": "image", "data": "AAAA", "mimeType": "image/png"}}}
		}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		_, _ = w.Write(append(data, '\n'))
	}
}

func TestTrimImagesKeepsLatest(t *testing.T) {
	img := func() chatMessage { return chatMessage{Role: "user", Content: []map[string]any{{"type": "image_url"}}} }
	out := trimImages([]chatMessage{img(), img(), img()}, 2)
	if _, ok := out[0].Content.(string); !ok {
		t.Error("oldest image kept")
	}
	if _, ok := out[2].Content.([]map[string]any); !ok {
		t.Error("latest image dropped")
	}
}
