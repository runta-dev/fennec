package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeGateway runs one scripted Discord Gateway per connection. script gets
// the connection index and the client's identify/resume payload.
func fakeGateway(t *testing.T, script func(i int, first gatewayPayload, conn *websocket.Conn)) string {
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
		"author": map[string]any{"id": author, "username": "user" + author, "global_name": "User " + author, "bot": bot},
		"member": map[string]any{"nick": ""},
	}
}

// threadSink is a board that remembers which conversation is which request.
type threadSink struct {
	mu        sync.Mutex
	tracked   map[string]int
	submits   []Feedback
	followUps []Feedback
	fail      bool
	closed    map[int]bool // requests no longer Queued or Working
}

func (s *threadSink) Submit(_ context.Context, f Feedback) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return 0, errors.New("board down")
	}
	s.submits = append(s.submits, f)
	n := len(s.submits)
	if f.Conversation != "" {
		s.tracked[f.Conversation] = n
	}
	return n, nil
}

func (s *threadSink) FollowUp(_ context.Context, f Feedback) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.tracked[f.Conversation]
	if ok {
		s.followUps = append(s.followUps, f)
	}
	return n, ok, nil
}

func (s *threadSink) Active(_ context.Context, n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed[n]
}

func (s *threadSink) close(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed == nil {
		s.closed = map[int]bool{}
	}
	s.closed[n] = true
}

func (s *threadSink) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.submits), len(s.followUps)
}

// discordAPI answers channel lookups and thread starts like Discord.
func discordAPI(t *testing.T, threadStatus int) (*httptest.Server, func() []apiCall) {
	return fakeAPI(t, func(_ int, c apiCall) (int, string) {
		switch {
		case c.Method == "GET" && c.Path == "/channels/555":
			return 200, `{"id":"555","type":0,"name":"general"}`
		case c.Method == "GET" && c.Path == "/channels/2002":
			return 200, `{"id":"2002","type":11,"parent_id":"777","name":"Old post"}`
		case c.Method == "POST" && strings.HasSuffix(c.Path, "/threads"):
			if threadStatus != 200 {
				return threadStatus, `{"message":"Missing Permissions","code":50013}`
			}
			id := strings.Split(c.Path, "/")[4]
			return 200, `{"id":"` + id + `","type":11}`
		}
		return 200, "{}"
	})
}

func typings(calls []apiCall, channel string) int {
	n := 0
	for _, c := range calls {
		if c.Method == "POST" && c.Path == "/channels/"+channel+"/typing" {
			n++
		}
	}
	return n
}

func posts(calls []apiCall, channel string) []apiCall {
	var out []apiCall
	for _, c := range calls {
		if c.Method == "POST" && c.Path == "/channels/"+channel+"/messages" {
			out = append(out, c)
		}
	}
	return out
}

func TestGatewayMentionsForumPostsAndFollowUps(t *testing.T) {
	identify := make(chan gatewayPayload, 1)
	wsURL := fakeGateway(t, func(i int, first gatewayPayload, conn *websocket.Conn) {
		identify <- first
		dispatch(conn, 1, "READY", map[string]any{"session_id": "s1", "user": map[string]any{"id": "900", "username": "fennec", "bot": true}})
		// An @mention in a channel: new thread on that message.
		m := message("1001", "555", "42", false, "<@900> App crashes on launch\nsince 1.4.2")
		m["member"] = map[string]any{"nick": "Eddie"}
		dispatch(conn, 2, "MESSAGE_CREATE", m)
		// Chatter without a mention: ignored.
		dispatch(conn, 3, "MESSAGE_CREATE", message("1002", "555", "43", false, "just chatting"))
		// Someone else in the new thread: follow-up, no mention needed.
		dispatch(conn, 4, "MESSAGE_CREATE", message("1003", "1001", "43", false, "same here on Android"))
		// The bot's own messages: ignored.
		dispatch(conn, 5, "MESSAGE_CREATE", message("1004", "1001", "900", true, "<@42> Thanks!"))
		// A new forum post.
		dispatch(conn, 6, "THREAD_CREATE", map[string]any{"id": "2001", "type": 11, "parent_id": "777", "name": "Login broken"})
		dispatch(conn, 7, "MESSAGE_CREATE", message("2001", "2001", "44", false, "can't log in with Google"))
		// A reply in a post from before the bot watched: ignored.
		dispatch(conn, 8, "MESSAGE_CREATE", message("3000", "2002", "45", false, "bump"))
		// A bare mention: asked to say what's going on, nothing filed.
		dispatch(conn, 9, "MESSAGE_CREATE", message("3001", "555", "46", false, "<@900>"))
		// A mention inside a tracked post: a follow-up, not a new request.
		dispatch(conn, 10, "MESSAGE_CREATE", message("3002", "2001", "44", false, "<@!900> any update?"))
		var p gatewayPayload
		conn.ReadJSON(&p) // hold until the client goes away
	})

	api, calls := discordAPI(t, 200)
	f := newFixture(t, func(c *Config) { c.APIBase = api.URL; c.GatewayURL = wsURL })
	sink := &threadSink{tracked: map[string]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.a.Run(ctx, sink) }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s, u := sink.counts()
		if s == 2 && u == 2 && len(posts(calls(), "555")) == 1 && typings(calls(), "2001") >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v", err)
	}

	id := <-identify
	var d map[string]any
	json.Unmarshal(id.D, &d)
	if id.Op != opIdentify || int(d["intents"].(float64)) != intentGuilds|intentGuildMessages|intentMessageContent {
		t.Errorf("identify = %+v", d)
	}

	if len(sink.submits) != 2 || len(sink.followUps) != 2 {
		t.Fatalf("submits %+v\nfollow-ups %+v", sink.submits, sink.followUps)
	}
	m := sink.submits[0]
	if m.Text != "App crashes on launch\nsince 1.4.2" || m.Conversation != "1001" || m.ReplyRef != "v1:1001:42" ||
		m.Author != (Author{ID: "42", Name: "Eddie", Handle: "user42"}) || m.Title != "" {
		t.Errorf("mention = %+v", m)
	}
	p := sink.submits[1]
	if p.Text != "can't log in with Google" || p.Title != "Login broken" || p.Conversation != "2001" || p.ReplyRef != "v1:2001:44" {
		t.Errorf("forum post = %+v", p)
	}
	if u := sink.followUps[0]; u.Conversation != "1001" || u.Text != "same here on Android" || u.Author.ID != "43" {
		t.Errorf("thread follow-up = %+v", u)
	}
	if u := sink.followUps[1]; u.Conversation != "2001" || u.Text != "any update?" {
		t.Errorf("post follow-up = %+v", u)
	}

	all := calls()
	var thread *apiCall
	for i, c := range all {
		if c.Path == "/channels/555/messages/1001/threads" {
			thread = &all[i]
		}
	}
	if thread == nil || thread.Body["name"] != "App crashes on launch" {
		t.Errorf("thread start = %+v", thread)
	}
	// No placeholder message: the bot shows that it is typing (FEN-242).
	if acks := posts(all, "1001"); len(acks) != 0 {
		t.Errorf("thread placeholder = %+v", acks)
	}
	if acks := posts(all, "2001"); len(acks) != 0 {
		t.Errorf("forum placeholder = %+v", acks)
	}
	if typings(all, "1001") == 0 || typings(all, "2001") == 0 {
		t.Errorf("no typing indicator: %+v", all)
	}
	bare := posts(all, "555")
	if len(bare) != 1 || bare[0].Body["content"] != "<@46> "+ackEmpty || bare[0].Body["message_reference"].(map[string]any)["message_id"] != "3001" {
		t.Errorf("bare mention answer = %+v", bare)
	}
	if len(posts(all, "2002")) != 0 {
		t.Error("an old forum post was answered")
	}
}

func TestMentionWithoutThreadPermissionAnswersUnderTheMessage(t *testing.T) {
	api, calls := discordAPI(t, 403)
	f := newFixture(t, func(c *Config) { c.APIBase = api.URL })
	f.a.botID = "900"
	sink := &threadSink{tracked: map[string]int{}}
	f.a.handle(context.Background(), sink, gatewayMessage{ID: "1001", ChannelID: "555", GuildID: "77", Content: "<@900> crash",
		Author: discordUser{ID: "42", Username: "ed"}})
	if len(sink.submits) != 1 || sink.submits[0].Conversation != "" || sink.submits[0].ReplyRef != "v1:555:42:1001" {
		t.Fatalf("submits = %+v", sink.submits)
	}
	if ack := posts(calls(), "555"); len(ack) != 0 || typings(calls(), "555") == 0 {
		t.Errorf("ack = %+v, typing %d", ack, typings(calls(), "555"))
	}
}

func TestMentionsOnlyInConfiguredChannelsAndServers(t *testing.T) {
	api, _ := discordAPI(t, 200)
	f := newFixture(t, func(c *Config) { c.APIBase = api.URL; c.Channels = []string{"999"}; c.Guilds = []string{"77"} })
	f.a.botID = "900"
	sink := &threadSink{tracked: map[string]int{}}
	f.a.handle(context.Background(), sink, gatewayMessage{ID: "1001", ChannelID: "555", GuildID: "77", Content: "<@900> crash", Author: discordUser{ID: "42"}})
	f.a.handle(context.Background(), sink, gatewayMessage{ID: "1002", ChannelID: "999", GuildID: "78", Content: "<@900> crash", Author: discordUser{ID: "42"}})
	if len(sink.submits) != 0 {
		t.Errorf("filed %+v", sink.submits)
	}
}

func TestBoardFailureIsToldInTheThread(t *testing.T) {
	api, calls := discordAPI(t, 200)
	f := newFixture(t, func(c *Config) { c.APIBase = api.URL })
	f.a.botID = "900"
	f.a.handle(context.Background(), &threadSink{tracked: map[string]int{}, fail: true},
		gatewayMessage{ID: "1001", ChannelID: "555", GuildID: "77", Content: "<@900> crash", Author: discordUser{ID: "42"}})
	if ack := posts(calls(), "1001"); len(ack) != 1 || ack[0].Body["content"] != "<@42> "+ackFailed {
		t.Errorf("ack = %+v", ack)
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
	err := f.a.Run(ctx, &threadSink{tracked: map[string]int{}})
	if !errors.Is(err, ErrFatalGateway) || !strings.Contains(err.Error(), "Message Content") {
		t.Errorf("Run = %v, want a fatal intents error", err)
	}
}

func TestTypingUntilTheBotAnswersOrTheRequestCloses(t *testing.T) {
	every := typingEvery
	typingEvery = 5 * time.Millisecond
	t.Cleanup(func() { typingEvery = every })
	api, calls := discordAPI(t, 200)
	f := newFixture(t, func(c *Config) { c.APIBase = api.URL })
	f.a.botID = "900"
	sink := &threadSink{tracked: map[string]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitTypings := func(channel string, n int) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if typings(calls(), channel) >= n {
				return
			}
		}
		t.Fatalf("%s: typing %d times, want %d", channel, typings(calls(), channel), n)
	}
	settled := func(channel string) int {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			before := typings(calls(), channel)
			time.Sleep(50 * time.Millisecond)
			if typings(calls(), channel) == before {
				return before
			}
		}
		t.Fatalf("%s: still typing", channel)
		return 0
	}

	// The bot answers in the thread: typing stops.
	f.a.handle(ctx, sink, gatewayMessage{ID: "1001", ChannelID: "555", GuildID: "77", Content: "<@900> crash", Author: discordUser{ID: "42"}})
	waitTypings("1001", 3)
	f.a.handle(ctx, sink, gatewayMessage{ID: "1002", ChannelID: "1001", GuildID: "77", Content: "<@42> Fixed in 1.2", Author: discordUser{ID: "900", Bot: true}})
	n := settled("1001")
	time.Sleep(30 * time.Millisecond)
	if typings(calls(), "1001") != n {
		t.Error("still typing after the bot answered")
	}

	// A follow-up in the thread: typing again, until the request closes.
	f.a.handle(ctx, sink, gatewayMessage{ID: "1003", ChannelID: "1001", GuildID: "77", Content: "still broken", Author: discordUser{ID: "42"}})
	waitTypings("1001", n+3)
	sink.close(1)
	n = settled("1001")
	time.Sleep(30 * time.Millisecond)
	if typings(calls(), "1001") != n {
		t.Error("still typing after the request closed")
	}
	f.a.mu.Lock()
	left := len(f.a.typing)
	f.a.mu.Unlock()
	if left != 0 {
		t.Errorf("%d typing loops left", left)
	}
}
