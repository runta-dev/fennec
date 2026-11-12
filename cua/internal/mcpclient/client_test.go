package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// fakeServer answers initialize, tools/list and tools/call; a call to "slow"
// never answers, and its cancel notification is reported on cancelled.
func fakeServer(t *testing.T, r io.Reader, w io.Writer, cancelled chan<- float64) {
	t.Helper()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			t.Error(err)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18"}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "observe"}, {"name": "act"}}}
		case "tools/call":
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name == "slow" {
				continue
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "hi " + p.Name}}}
		case "notifications/cancelled":
			var p struct {
				RequestID float64 `json:"requestId"`
			}
			_ = json.Unmarshal(req.Params, &p)
			cancelled <- p.RequestID
			continue
		default:
			continue
		}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		_, _ = w.Write(append(data, '\n'))
	}
}

func TestClientRoundTripAndCancel(t *testing.T) {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	cancelled := make(chan float64, 1)
	go fakeServer(t, sr, sw, cancelled)
	c := New(cw, cr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	names, err := c.Tools(ctx)
	if err != nil || len(names) != 2 || names[0] != "observe" {
		t.Fatalf("tools %v %v", names, err)
	}
	r, err := c.Tool(ctx, "act", map[string]any{})
	if err != nil || r.Text() != "hi act" {
		t.Fatalf("call %+v %v", r, err)
	}
	slow, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	if _, err := c.Tool(slow, "slow", nil); err == nil {
		t.Fatal("slow call returned")
	}
	select {
	case id := <-cancelled:
		if id != 4 {
			t.Errorf("cancelled id %v, want 4", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no cancel notification")
	}
	_ = cw.Close()
}
