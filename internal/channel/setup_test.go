package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

var testKey = strings.Repeat("ab", 32)

func fakeDiscord(t *testing.T, flags int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/applications/@me":
			json.NewEncoder(w).Encode(map[string]any{"id": "111111111111111111", "name": "Nebula Bot", "verify_key": testKey, "flags": flags})
		case "/channels/222222222222222222":
			json.NewEncoder(w).Encode(map[string]any{"name": "bug-report", "guild_id": "333333333333333333"})
		case "/guilds/333333333333333333":
			json.NewEncoder(w).Encode(map[string]any{"name": "Nebula Run"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := DiscordAPIBase
	DiscordAPIBase = srv.URL
	t.Cleanup(func() { DiscordAPIBase = old })
}

func discordReq(token string, channels ...string) SetupRequest {
	return SetupRequest{Project: "nebula", ID: "discord-ab12", Kind: KindDiscord, Discord: &DiscordSetup{
		ApplicationID: "111111111111111111", PublicKey: testKey, BotToken: token, Channels: channels}}
}

func TestSetupDiscord(t *testing.T) {
	home := t.TempDir()
	fakeDiscord(t, 1<<19)
	ctx := context.Background()
	for name, c := range map[string]struct {
		req  SetupRequest
		code string
	}{
		"bad token":   {discordReq("nope"), "discord_token"},
		"bad channel": {discordReq("good-token", "999999999999999999"), "discord_channel"},
		"format":      {SetupRequest{Project: "nebula", ID: "discord-ab12", Kind: KindDiscord, Discord: &DiscordSetup{ApplicationID: "x", PublicKey: testKey, BotToken: "t"}}, "discord_format"},
	} {
		res, err := Setup(ctx, home, c.req)
		if err != nil || res.OK || res.Code != c.code {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	if _, err := os.Stat(ConfigPath(home)); !os.IsNotExist(err) {
		t.Fatal("a failed check saved the config")
	}
	res, err := Setup(ctx, home, discordReq("good-token", "222222222222222222"))
	if err != nil || !res.OK || res.Source != "Nebula Run · #bug-report, /feedback" {
		t.Fatalf("%+v %v", res, err)
	}
	st, _ := os.Stat(ConfigPath(home))
	if st.Mode().Perm() != 0o600 {
		t.Fatal("config mode", st.Mode())
	}
	// Editing without the token keeps the saved one.
	if res, err = Setup(ctx, home, discordReq("", "222222222222222222")); err != nil || !res.OK {
		t.Fatalf("keep token: %+v %v", res, err)
	}
	info, err := Info(home, "discord-ab12")
	if err != nil || !info.HasSecret || info.Discord.BotToken != "" || info.Discord.Channels[0] != "222222222222222222" {
		t.Fatalf("%+v %v", info, err)
	}
	data, _ := os.ReadFile(ConfigPath(home))
	if !strings.Contains(string(data), "good-token") {
		t.Fatal("token not saved")
	}
	// Same id from another project is refused.
	other := discordReq("good-token")
	other.Project = "other"
	if res, _ = Setup(ctx, home, other); res.Code != "taken" {
		t.Fatalf("%+v", res)
	}
	if err = Remove(home, "discord-ab12"); err != nil {
		t.Fatal(err)
	}
	if info, _ = Info(home, "discord-ab12"); info.Kind != "" {
		t.Fatal("not removed", info)
	}
}

func TestSetupDiscordNeedsMessageContent(t *testing.T) {
	fakeDiscord(t, 0)
	res, err := Setup(context.Background(), t.TempDir(), discordReq("good-token", "222222222222222222"))
	if err != nil || res.Code != "discord_message_content" {
		t.Fatalf("%+v %v", res, err)
	}
	// /feedback alone doesn't need it.
	res, err = Setup(context.Background(), t.TempDir(), discordReq("good-token"))
	if err != nil || !res.OK || res.Source != "Nebula Bot · /feedback" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestSetupWidget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		codes := []string{"invalid-input-response"}
		if r.Form.Get("secret") != "real-secret" {
			codes = []string{"invalid-input-secret"}
		}
		json.NewEncoder(w).Encode(map[string]any{"success": false, "error-codes": codes})
	}))
	defer srv.Close()
	old := TurnstileVerifyURL
	TurnstileVerifyURL = srv.URL
	defer func() { TurnstileVerifyURL = old }()
	home := t.TempDir()
	os.MkdirAll(Dir(home), 0o700)
	os.WriteFile(ConfigPath(home), []byte(`{"public_url":"https://in.example.com/","channels":[]}`), 0o600)
	req := func(secret string, origins ...string) SetupRequest {
		return SetupRequest{Project: "nebula", ID: "widget-cd34", Kind: KindWidget, Widget: &WidgetSetup{Origins: origins, SiteKey: "site", Secret: secret}}
	}
	ctx := context.Background()
	for code, r := range map[string]SetupRequest{
		"widget_origins":   req("real-secret"),
		"widget_origin":    req("real-secret", "example.com"),
		"widget_secret":    req("wrong", "https://example.com"),
		"widget_turnstile": req("", "https://example.com"),
	} {
		if res, err := Setup(ctx, home, r); err != nil || res.Code != code {
			t.Errorf("%s: %+v %v", code, res, err)
		}
	}
	res, err := Setup(ctx, home, req("real-secret", "https://nebularun.com", "https://*.nebularun.com"))
	if err != nil || !res.OK || res.Source != "nebularun.com · *.nebularun.com" || !strings.Contains(res.Snippet, `src="https://in.example.com/widget.js"`) {
		t.Fatalf("%+v %v", res, err)
	}
	cfg, err := LoadConfig(home)
	if err != nil || cfg.PublicURL != "https://in.example.com/" || len(cfg.Channels) != 1 {
		t.Fatalf("%+v %v", cfg, err)
	}
}
