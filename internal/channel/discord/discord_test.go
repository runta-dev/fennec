package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

type fixture struct{ a *Adapter }

func newFixture(t *testing.T, mutate func(*Config)) *fixture {
	t.Helper()
	cfg := Config{BotToken: "tok", Forums: []string{"777"}}
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{a: a}
}

func TestNewRejectsBadConfig(t *testing.T) {
	for name, c := range map[string]Config{
		"no token":    {BotToken: " "},
		"bad forum":   {BotToken: "t", Forums: []string{"#bugs"}},
		"bad channel": {BotToken: "t", Channels: []string{"general"}},
		"bad guild":   {BotToken: "t", Guilds: []string{"x"}},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("%s: New accepted %+v", name, c)
		}
	}
	a, err := New(Config{BotToken: "t", Forums: []string{"777"}})
	if err != nil || a.cfg.BotToken != "Bot t" {
		t.Fatalf("New = %+v, %v", a, err)
	}
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
