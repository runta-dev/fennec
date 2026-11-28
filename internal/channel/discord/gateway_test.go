package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeGateway runs one scripted Discord Gateway per connection. script gets
// the connection index and the client's identify/resume payload.
func fakeGateway(t *testing.T, script func(i int, hello gatewayPayload, conn *websocket.Conn)) string {
	t.Helper()
	up := websocket.Upgrader{}
	conns := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "10" || r.URL.Query().Get("encoding") != "json" {
			t.Errorf("gateway query = %q", r.URL.RawQuery)
		}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		i := conns
		conns++
		conn.WriteJSON(map[string]any{"op": opHello, "d": map[string]any{"heartbeat_interval": 60_000}})
		var first gatewayPayload
		if err := conn.ReadJSON(&first); err != nil {
			return
		}
		script(i, first, conn)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dispatch(conn *websocket.Conn, seq int, t string, d any) {
	conn.WriteJSON(map[string]any{"op": opDispatch, "s": seq, "t": t, "d": d})
}

func message(id, channel, author string, bot bool, content string) map[string]any {
	return map[string]any{
		"id": id, "channel_id": channel, "guild_id": "77", "type": 0, "content": content,
		"author": map[string]any{"id": author, "username": "edward", "global_name": "Edward", "bot": bot},
		"member": map[string]any{"nick": "Eddie"},
	}
}

func TestGatewayFilesWatchedMessagesAndResumes(t *testing.T) {
	type start struct {
		op int
		d  map[string]any
	}
	starts := make(chan start, 4)
	var wsURL string
	wsURL = fakeGateway(t, func(i int, first gatewayPayload, conn *websocket.Conn) {
		var d map[string]any
		_ = json.Unmarshal(first.D, &d)
		starts <- start{first.Op, d}
		switch i {
		case 0:
			dispatch(conn, 1, "READY", map[string]any{"session_id": "sess-1", "resume_gateway_url": wsURL})
			dispatch(conn, 2, "MESSAGE_CREATE", message("1001", "555", "42", false, "  Sync is stuck  "))
			dispatch(conn, 3, "MESSAGE_CREATE", message("1002", "555", "50", true, "bot noise"))
			dispatch(conn, 4, "MESSAGE_CREATE", message("1003", "999", "42", false, "unwatched channel"))
			dispatch(conn, 5, "MESSAGE_CREATE", message("1004", "555", "42", false, "   "))
			conn.WriteJSON(map[string]any{"op": opReconnect})
			time.Sleep(200 * time.Millisecond)
		case 1:
			dispatch(conn, 6, "RESUMED", nil)
			dispatch(conn, 7, "MESSAGE_CREATE", message("1005", "555", "43", false, "second one"))
			var p gatewayPayload
			conn.ReadJSON(&p) // hold until the client goes away
		}
	})

	api, calls := fakeAPI(t, nil)
	f := newFixture(t, func(c *Config) { c.APIBase = api.URL; c.GatewayURL = wsURL })
	sink := &recordingSink{n: 1}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.a.Run(ctx, sink) }()

	deadline := time.Now().Add(10 * time.Second)
	for len(sink.all()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v", err)
	}

	id := <-starts
	if id.op != opIdentify || id.d["token"] != "tok" || int(id.d["intents"].(float64)) != intentGuildMessages|intentMessageContent {
		t.Errorf("identify = %+v", id)
	}
	res := <-starts
	if res.op != opResume || res.d["session_id"] != "sess-1" || res.d["seq"].(float64) != 5 {
		t.Errorf("resume = %+v", res)
	}

	got := sink.all()
	if len(got) != 2 {
		t.Fatalf("filed %d messages, want 2: %+v", len(got), got)
	}
	fb := got[0]
	if fb.Text != "Sync is stuck" || fb.Author != (Author{ID: "42", Name: "Eddie", Handle: "edward"}) ||
		fb.ReplyRef != "v1:555:42:1001" || fb.Context["source"] != "message" ||
		fb.Context["link"] != "https://discord.com/channels/77/555/1001" {
		t.Errorf("feedback = %+v", fb)
	}
	if got[1].ReplyRef != "v1:555:43:1005" {
		t.Errorf("second = %+v", got[1])
	}

	time.Sleep(100 * time.Millisecond) // receipt reactions run after Submit
	var reactions int
	for _, c := range calls() {
		if c.Method == "PUT" && strings.HasPrefix(c.Path, "/channels/555/messages/100") && strings.HasSuffix(c.Path, "/@me") {
			reactions++
		}
	}
	if reactions != 2 {
		t.Errorf("receipt reactions = %d, want 2 (%+v)", reactions, calls())
	}
}

func TestGatewayStopsOnDisallowedIntents(t *testing.T) {
	wsURL := fakeGateway(t, func(_ int, _ gatewayPayload, conn *websocket.Conn) {
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4014, "Disallowed intent(s)."))
		time.Sleep(100 * time.Millisecond)
	})
	f := newFixture(t, func(c *Config) { c.GatewayURL = wsURL })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := f.a.Run(ctx, &recordingSink{})
	if !errors.Is(err, ErrFatalGateway) || !strings.Contains(err.Error(), "Message Content") {
		t.Errorf("Run = %v, want a fatal intents error", err)
	}
}
