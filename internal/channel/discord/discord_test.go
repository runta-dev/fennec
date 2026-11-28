package discord

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type fixture struct {
	a    *Adapter
	priv ed25519.PrivateKey
	now  time.Time
}

func newFixture(t *testing.T, mutate func(*Config)) *fixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ApplicationID: "1000",
		PublicKey:     hex.EncodeToString(pub),
		BotToken:      "tok",
		Channels:      []string{"555"},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }
	return &fixture{a: a, priv: priv, now: now}
}

// post sends body to the interactions handler, signed at ts.
func (f *fixture) post(t *testing.T, sink Sink, body string, ts time.Time, tamper bool) *httptest.ResponseRecorder {
	t.Helper()
	stamp := strconv.FormatInt(ts.Unix(), 10)
	sig := ed25519.Sign(f.priv, []byte(stamp+body))
	if tamper {
		body += " "
	}
	req := httptest.NewRequest(http.MethodPost, "/discord/interactions", strings.NewReader(body))
	req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(sig))
	req.Header.Set("X-Signature-Timestamp", stamp)
	rec := httptest.NewRecorder()
	f.a.InteractionsHandler(sink).ServeHTTP(rec, req)
	return rec
}

type recordingSink struct {
	mu   sync.Mutex
	got  []Feedback
	n    int
	err  error
	wait time.Duration
}

func (s *recordingSink) Submit(ctx context.Context, f Feedback) (int, error) {
	if s.wait > 0 {
		time.Sleep(s.wait)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, f)
	return s.n, s.err
}

func (s *recordingSink) all() []Feedback {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Feedback(nil), s.got...)
}

func TestNewRejectsBadConfig(t *testing.T) {
	ok := Config{ApplicationID: "1", PublicKey: strings.Repeat("ab", 32), BotToken: "t"}
	for name, mut := range map[string]func(*Config){
		"short key":   func(c *Config) { c.PublicKey = "abcd" },
		"no token":    func(c *Config) { c.BotToken = " " },
		"no app":      func(c *Config) { c.ApplicationID = "" },
		"bad channel": func(c *Config) { c.Channels = []string{"#general"} },
	} {
		c := ok
		mut(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s: New accepted %+v", name, c)
		}
	}
	if _, err := New(ok); err != nil {
		t.Fatal(err)
	}
}

func TestPingNeedsValidSignature(t *testing.T) {
	f := newFixture(t, nil)
	sink := &recordingSink{}
	ping := `{"type":1}`

	if rec := f.post(t, sink, ping, f.now, false); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"type":1}` {
		t.Fatalf("valid ping: %d %s", rec.Code, rec.Body)
	}
	if rec := f.post(t, sink, ping, f.now, true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered body: got %d, want 401", rec.Code)
	}
	if rec := f.post(t, sink, ping, f.now.Add(-10*time.Minute), false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale timestamp: got %d, want 401", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(ping))
	rec := httptest.NewRecorder()
	f.a.InteractionsHandler(sink).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned: got %d, want 401", rec.Code)
	}
	if len(sink.all()) != 0 {
		t.Fatal("ping must not file anything")
	}
}

const feedbackCommand = `{"type":2,"guild_id":"77","channel_id":"888",
 "member":{"nick":"Eddie","user":{"id":"42","username":"edward","global_name":"Edward"}},
 "data":{"name":"feedback","options":[{"name":"text","type":3,"value":"  App crashes on launch  "}]}}`

type response struct {
	Type int `json:"type"`
	Data struct {
		Content string `json:"content"`
		Flags   int    `json:"flags"`
	} `json:"data"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) response {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var r response
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Type != responseMessage || r.Data.Flags != messageFlagEphemeral {
		t.Fatalf("want an ephemeral message, got %+v", r)
	}
	return r
}

func TestFeedbackCommandFilesRequest(t *testing.T) {
	f := newFixture(t, nil)
	sink := &recordingSink{n: 226}
	r := decode(t, f.post(t, sink, feedbackCommand, f.now, false))
	if !strings.Contains(r.Data.Content, "#226") {
		t.Errorf("ack %q should name the request", r.Data.Content)
	}
	got := sink.all()
	if len(got) != 1 {
		t.Fatalf("filed %d, want 1", len(got))
	}
	fb := got[0]
	want := Author{ID: "42", Name: "Eddie", Handle: "edward"}
	if fb.Channel != "discord" || fb.Author != want || fb.Text != "App crashes on launch" {
		t.Errorf("feedback = %+v", fb)
	}
	if fb.Context["source"] != "command" || fb.Context["channel_id"] != "888" || fb.Context["guild_id"] != "77" {
		t.Errorf("context = %v", fb.Context)
	}
	if fb.ReplyRef != "v1:888:42" {
		t.Errorf("reply ref = %q", fb.ReplyRef)
	}
}

func TestFeedbackCommandEdgeCases(t *testing.T) {
	t.Run("empty text", func(t *testing.T) {
		f := newFixture(t, nil)
		sink := &recordingSink{}
		body := strings.Replace(feedbackCommand, "  App crashes on launch  ", "   ", 1)
		if r := decode(t, f.post(t, sink, body, f.now, false)); r.Data.Content != ackEmpty {
			t.Errorf("ack = %q", r.Data.Content)
		}
		if len(sink.all()) != 0 {
			t.Error("empty feedback was filed")
		}
	})
	t.Run("other server", func(t *testing.T) {
		f := newFixture(t, func(c *Config) { c.Guilds = []string{"99"} })
		sink := &recordingSink{}
		if r := decode(t, f.post(t, sink, feedbackCommand, f.now, false)); r.Data.Content != ackWrongServer {
			t.Errorf("ack = %q", r.Data.Content)
		}
		if len(sink.all()) != 0 {
			t.Error("feedback from an unlisted server was filed")
		}
	})
	t.Run("dm uses user", func(t *testing.T) {
		f := newFixture(t, nil)
		sink := &recordingSink{n: 3}
		body := `{"type":2,"channel_id":"900","user":{"id":"43","username":"amy"},
		 "data":{"name":"feedback","options":[{"name":"text","type":3,"value":"hi"}]}}`
		decode(t, f.post(t, sink, body, f.now, false))
		got := sink.all()
		if len(got) != 1 || got[0].Author != (Author{ID: "43", Name: "amy", Handle: "amy"}) || got[0].ReplyRef != "v1:900:43" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("sink fails", func(t *testing.T) {
		f := newFixture(t, nil)
		sink := &recordingSink{err: errors.New("board down")}
		if r := decode(t, f.post(t, sink, feedbackCommand, f.now, false)); r.Data.Content != ackFailed {
			t.Errorf("ack = %q", r.Data.Content)
		}
	})
	t.Run("slow sink still answers in time", func(t *testing.T) {
		old := submitBudget
		submitBudget = 50 * time.Millisecond
		defer func() { submitBudget = old }()
		f := newFixture(t, nil)
		sink := &recordingSink{n: 9, wait: 300 * time.Millisecond}
		start := time.Now()
		r := decode(t, f.post(t, sink, feedbackCommand, f.now, false))
		if time.Since(start) > 250*time.Millisecond || r.Data.Content != ackQueued {
			t.Errorf("ack %q after %s", r.Data.Content, time.Since(start))
		}
		deadline := time.Now().Add(2 * time.Second)
		for len(sink.all()) == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if len(sink.all()) != 1 {
			t.Error("the slow submit should still finish")
		}
	})
}

type apiCall struct {
	Method, Path, Auth string
	Body               map[string]any
}

// fakeAPI records REST calls; respond picks the status for call i.
func fakeAPI(t *testing.T, respond func(i int, c apiCall) (int, string)) (*httptest.Server, func() []apiCall) {
	var mu sync.Mutex
	var calls []apiCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		c := apiCall{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization")}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &c.Body)
		}
		mu.Lock()
		i := len(calls)
		calls = append(calls, c)
		mu.Unlock()
		code, body := 200, "{}"
		if respond != nil {
			code, body = respond(i, c)
		}
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []apiCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]apiCall(nil), calls...)
	}
}

func TestReplyMentionsAuthorInSourceChannel(t *testing.T) {
	srv, calls := fakeAPI(t, nil)
	f := newFixture(t, func(c *Config) { c.APIBase = srv.URL })
	if err := f.a.Reply(context.Background(), "v1:555:42:777", "Fixed in 1.4.2 @everyone"); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 1 {
		t.Fatalf("calls = %d", len(got))
	}
	c := got[0]
	if c.Method != "POST" || c.Path != "/channels/555/messages" || c.Auth != "Bot tok" {
		t.Errorf("call = %+v", c)
	}
	if c.Body["content"] != "<@42> Fixed in 1.4.2 @everyone" {
		t.Errorf("content = %v", c.Body["content"])
	}
	am := c.Body["allowed_mentions"].(map[string]any)
	if len(am["parse"].([]any)) != 0 || am["users"].([]any)[0] != "42" {
		t.Errorf("allowed_mentions = %v (must only allow the author)", am)
	}
	ref := c.Body["message_reference"].(map[string]any)
	if ref["message_id"] != "777" || ref["fail_if_not_exists"] != false {
		t.Errorf("message_reference = %v", ref)
	}
	if n, _ := c.Body["nonce"].(string); len(n) != 25 || c.Body["enforce_nonce"] != true {
		t.Errorf("nonce = %v", c.Body["nonce"])
	}

	// A /feedback ref has no message: post in the channel without replying.
	if err := f.a.Reply(context.Background(), "v1:888:42", "Thanks"); err != nil {
		t.Fatal(err)
	}
	if _, has := calls()[1].Body["message_reference"]; has {
		t.Error("command reply should not reference a message")
	}
}

func TestReplySplitsLongText(t *testing.T) {
	srv, calls := fakeAPI(t, nil)
	f := newFixture(t, func(c *Config) { c.APIBase = srv.URL })
	text := strings.Repeat("Café crème brûlée. ", 400) // 8400 bytes, multibyte runes
	if err := f.a.Reply(context.Background(), "v1:555:42:777", text); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) < 4 {
		t.Fatalf("calls = %d", len(got))
	}
	var joined []string
	for i, c := range got {
		content := c.Body["content"].(string)
		if len(content) > maxMessageLength || !utf8.ValidString(content) {
			t.Errorf("part %d: %d bytes, valid=%v", i, len(content), utf8.ValidString(content))
		}
		_, ref := c.Body["message_reference"]
		if (i == 0) != strings.HasPrefix(content, "<@42> ") || (i == 0) != ref {
			t.Errorf("part %d: only the first part mentions and replies", i)
		}
		joined = append(joined, strings.TrimPrefix(content, "<@42> "))
	}
	// Parts are cut at spaces, which the split drops.
	if strings.Join(joined, " ") != strings.TrimSpace(text) {
		t.Error("parts don't add up to the text")
	}
}

func TestReplyRetriesAndGivesUp(t *testing.T) {
	t.Run("rate limited then ok", func(t *testing.T) {
		srv, calls := fakeAPI(t, func(i int, _ apiCall) (int, string) {
			if i == 0 {
				return 429, `{"retry_after":0.01,"global":false}`
			}
			return 200, "{}"
		})
		f := newFixture(t, func(c *Config) { c.APIBase = srv.URL })
		if err := f.a.Reply(context.Background(), "v1:555:42", "hi"); err != nil {
			t.Fatal(err)
		}
		if len(calls()) != 2 {
			t.Errorf("calls = %d, want 2", len(calls()))
		}
	})
	t.Run("missing access is permanent", func(t *testing.T) {
		srv, calls := fakeAPI(t, func(int, apiCall) (int, string) { return 403, `{"message":"Missing Access","code":50001}` })
		f := newFixture(t, func(c *Config) { c.APIBase = srv.URL })
		err := f.a.Reply(context.Background(), "v1:555:42", "hi")
		if !errors.Is(err, ErrPermanent) || len(calls()) != 1 {
			t.Errorf("err = %v after %d calls", err, len(calls()))
		}
	})
	t.Run("bad refs", func(t *testing.T) {
		f := newFixture(t, nil)
		for _, ref := range []string{"", "v1:555", "v2:555:42", "v1:555:<@here>", "v1:555:42:7:8"} {
			if err := f.a.Reply(context.Background(), ref, "hi"); !errors.Is(err, ErrPermanent) {
				t.Errorf("ref %q: err = %v", ref, err)
			}
		}
	})
}

func TestRegisterCommands(t *testing.T) {
	srv, calls := fakeAPI(t, nil)
	f := newFixture(t, func(c *Config) { c.APIBase = srv.URL; c.Guilds = []string{"77"} })
	if err := f.a.RegisterCommands(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := calls()[0]
	if c.Method != "PUT" || c.Path != "/applications/1000/guilds/77/commands" {
		t.Errorf("call = %s %s", c.Method, c.Path)
	}
}

func TestSplitMessage(t *testing.T) {
	if got := splitMessage("short", 10); len(got) != 1 || got[0] != "short" {
		t.Errorf("got %q", got)
	}
	text := strings.Repeat("word ", 100)
	for _, p := range splitMessage(text, 60) {
		if len(p) > 60 || strings.HasPrefix(p, " ") || strings.HasSuffix(p, " ") {
			t.Errorf("bad part %q", p)
		}
	}
	// No separator at all, multibyte: must not split a rune.
	for _, p := range splitMessage(strings.Repeat("é", 50), 31) {
		if !utf8.ValidString(p) || len(p) > 31 {
			t.Errorf("bad part %q", p)
		}
	}
}
