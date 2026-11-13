//go:build egolive

package ego

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Live tests against the real ego lite: they open only their own task
// space, load only data: URLs, and always finish the space.

func liveClient(t *testing.T) *Client {
	bin := FindBin()
	if bin == "" {
		t.Skip("ego-browser not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "fcego")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return &Client{Dir: dir, Start: ExecStarter(bin, t.Logf)}
}

func liveCall(t *testing.T, m *Manager, key any, req map[string]any) map[string]any {
	t.Helper()
	data, _ := json.Marshal(req)
	v, perr := m.Call(context.Background(), key, data)
	if perr != nil {
		t.Fatalf("%v: %s", req, perr.Message)
	}
	out, _ := json.Marshal(v)
	var res map[string]any
	_ = json.Unmarshal(out, &res)
	return res
}

// TestLiveManager covers hand-off and taking back, a bridge crash and
// restart, and the end of a turn closing unmarked tabs.
func TestLiveManager(t *testing.T) {
	c := liveClient(t)
	m := NewManager(Options{Bin: FindBin(), Dir: c.Dir, Log: t.Logf})
	key := "live"
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		m.CloseAll(ctx)
	}()
	page := "data:text/html,<title>One</title><input aria-label=Field><button>Go</button>"
	res := liveCall(t, m, key, map[string]any{"op": "createTab", "browserId": "ego", "url": page, "sessionName": "fennec-cua live manager"})
	b1 := res["binding"]
	if !strings.Contains(res["text"].(string), "button Go") {
		t.Fatalf("state %v", res["text"])
	}
	// Hand the space to the user, then take it back on the next call.
	liveCall(t, m, key, map[string]any{"op": "tab", "binding": b1, "method": "markHandoff"})
	res = liveCall(t, m, key, map[string]any{"op": "tab", "binding": b1, "method": "getAXState", "args": []any{map[string]any{"disableDiffing": true}}})
	if !strings.Contains(res["text"].(string), "button Go") {
		t.Fatalf("after take-back %v", res["text"])
	}
	// A crashed bridge restarts and reopens the space.
	s := m.session(key, false)
	s.client.mu.Lock()
	s.client.conn.proc.Kill()
	s.client.mu.Unlock()
	<-s.client.conn.dead
	gen := s.client.Gen()
	res = liveCall(t, m, key, map[string]any{"op": "tab", "binding": b1, "method": "getAXState", "args": []any{map[string]any{"disableDiffing": true}}})
	if !strings.Contains(res["text"].(string), "button Go") || s.client.Gen() != gen+1 {
		t.Fatalf("after restart (gen %d -> %d) %v", gen, s.client.Gen(), res["text"])
	}
	// Typing into the field by index, then a second tab that the turn's end
	// closes, while the marked first one stays.
	liveCall(t, m, key, map[string]any{"op": "tab", "binding": b1, "method": "typeText", "args": []any{2, "abc"}})
	res = liveCall(t, m, key, map[string]any{"op": "tab", "binding": b1, "method": "getAXState"})
	if !strings.Contains(res["text"].(string), "Value: abc") {
		t.Errorf("typed %v", res["text"])
	}
	liveCall(t, m, key, map[string]any{"op": "tab", "binding": b1, "method": "markDeliverable"})
	liveCall(t, m, key, map[string]any{"op": "createTab", "browserId": "ego", "url": "data:text/html,<title>Two</title>two"})
	m.TurnEnded(context.Background(), key)
	res = liveCall(t, m, key, map[string]any{"op": "listTabs"})
	tabs, _ := json.Marshal(res["tabs"])
	if strings.Contains(string(tabs), `"title":"Two"`) || !strings.Contains(string(tabs), `"title":"One"`) {
		t.Errorf("after the turn: %s", tabs)
	}
	// The marks were for that turn; finishing keeps nothing.
	for _, tb := range s.tabs {
		if tb.mark != "" {
			t.Errorf("mark %q survived the turn", tb.mark)
		}
	}
}

func TestLiveBridgeLifecycle(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	start := time.Now()
	if _, _, err := c.Do(ctx, "spaces", nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("first call (bridge start) %v", time.Since(start))
	start = time.Now()
	v, _, err := c.Do(ctx, "space", map[string]any{"name": "fennec-cua live bridge"})
	if err != nil {
		t.Fatal(err)
	}
	var sp struct {
		SpaceID   int    `json:"spaceId"`
		Ownership string `json:"ownership"`
	}
	_ = json.Unmarshal(v, &sp)
	defer func() {
		if _, _, err := c.Do(ctx, "call", map[string]any{"spaceId": sp.SpaceID, "method": "finish", "args": []any{map[string]any{"keep": []string{}}}}); err != nil {
			t.Errorf("finish: %v", err)
		}
		start := time.Now()
		c.Close()
		t.Logf("close %v", time.Since(start))
	}()
	if sp.Ownership != "agent" {
		t.Fatalf("space %s", v)
	}
	if _, _, err := c.Do(ctx, "call", map[string]any{"spaceId": sp.SpaceID, "page": "p1", "method": "goto", "args": []any{"data:text/html,<title>x</title><button>Go</button>"}}); err != nil {
		t.Fatal(err)
	}
	t.Logf("space + goto %v", time.Since(start))
	start = time.Now()
	v, _, err = c.Do(ctx, "call", map[string]any{"spaceId": sp.SpaceID, "page": "p1", "method": "cdp", "args": []any{"Accessibility.getFullAXTree", map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := ParseAXTree(v)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AX tree %v, %d nodes", time.Since(start), len(tree.Nodes))
	text := NewMemory().Read("1", "x", "data:text/html,x", tree, nil, false)
	if !strings.Contains(text, "button Go") {
		t.Errorf("state %s", text)
	}
	// An SDK error comes back as a RemoteError and the bridge stays up.
	if _, _, err := c.Do(ctx, "call", map[string]any{"spaceId": sp.SpaceID, "page": "p1", "method": "nope"}); err == nil {
		t.Error("an unknown method answered")
	}
	gen := c.Gen()
	if _, _, err := c.Do(ctx, "call", map[string]any{"spaceId": sp.SpaceID, "page": "p1", "method": "title"}); err != nil || c.Gen() != gen {
		t.Errorf("after an error: %v gen %d -> %d", err, gen, c.Gen())
	}
}
