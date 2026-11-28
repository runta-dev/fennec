package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"fennec/internal/board"
)

// fakeDiscord serves the parts of Discord's API the setup uses; the bot is
// in one server with a text channel and a forum. PATCH /applications/@me
// changes the flags, as the real one does for the limited intents.
func fakeDiscord(t *testing.T, flags int) *int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /applications/@me":
			json.NewEncoder(w).Encode(map[string]any{"id": "111111111111111111", "name": "Nebula App", "flags": flags, "bot": map[string]string{"username": "Nebula Bot"}})
		case "PATCH /applications/@me":
			var body struct{ Flags int }
			json.NewDecoder(r.Body).Decode(&body)
			flags = body.Flags
			w.Write([]byte("{}"))
		case "GET /users/@me/guilds":
			json.NewEncoder(w).Encode([]map[string]string{{"id": "333333333333333333", "name": "Nebula Run"}})
		case "GET /guilds/333333333333333333/channels":
			json.NewEncoder(w).Encode([]map[string]any{{"id": "222222222222222222", "name": "general", "type": 0}, {"id": "444444444444444444", "name": "feedback", "type": 15}, {"id": "5", "name": "Voice", "type": 2}})
		case "GET /channels/222222222222222222":
			json.NewEncoder(w).Encode(map[string]any{"name": "general", "type": 0, "guild_id": "333333333333333333"})
		case "GET /channels/444444444444444444":
			json.NewEncoder(w).Encode(map[string]any{"name": "feedback", "type": 15, "guild_id": "333333333333333333"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := DiscordAPIBase
	DiscordAPIBase = srv.URL
	t.Cleanup(func() { DiscordAPIBase = old })
	return &flags
}

// boardHome is a cloud home with the project "nebula".
func boardHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	p, err := board.Store{Home: home, Role: "cloud"}.CreateProject(board.Project{Name: "Nebula", Key: "NB", Color: "#6b7cff"})
	if err != nil {
		t.Fatal(err)
	}
	return home, p.ID
}

func TestSetupDiscordWithOnlyTheToken(t *testing.T) {
	home, project := boardHome(t)
	flags := fakeDiscord(t, 0)
	ctx := context.Background()
	req := func(token string, forums ...string) SetupRequest {
		return SetupRequest{Project: project, ID: "discord-ab12", Kind: KindDiscord, Discord: &DiscordSetup{BotToken: token, Forums: forums}}
	}
	for name, c := range map[string]struct {
		req  SetupRequest
		code string
	}{
		"bad token": {req("nope"), "discord_token"},
		"unseen":    {req("good-token", "999999999999999999"), "discord_channel"},
		"not forum": {req("good-token", "222222222222222222"), "discord_not_forum"},
	} {
		res, err := Setup(ctx, home, c.req)
		if err != nil || res.OK || res.Code != c.code {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	if _, err := os.Stat(ConfigPath(home)); !os.IsNotExist(err) {
		t.Fatal("a failed check saved the config")
	}
	// The token alone is enough; Message Content gets switched on.
	res, err := Setup(ctx, home, req("good-token"))
	if err != nil || !res.OK || res.Source != "Nebula Run · @Nebula Bot" || *flags != 1<<19 {
		t.Fatalf("%+v %v flags=%d", res, err, *flags)
	}
	if !strings.Contains(res.Discord.InviteURL, "client_id=111111111111111111") {
		t.Fatal(res.Discord.InviteURL)
	}
	st, _ := os.Stat(ConfigPath(home))
	if st.Mode().Perm() != 0o600 {
		t.Fatal("config mode", st.Mode())
	}
	// The project got its row, tags included, and no secret.
	p, _ := board.Store{Home: home, Role: "cloud"}.Project(project)
	if len(p.Channels) != 1 || p.Channels[0].Source != res.Source || !p.Channels[0].On || strings.Join(p.Channels[0].Tags, ",") != "feedback,discord" {
		t.Fatalf("%+v", p.Channels)
	}
	if raw, _ := json.Marshal(p); strings.Contains(string(raw), "good-token") {
		t.Fatal("token on the board")
	}
	// The agent picks the forum; the saved token is kept.
	var out strings.Builder
	if err = discordCommand(ctx, home, []string{"set", "discord-ab12", "forums=444444444444444444"}, &out); err != nil {
		t.Fatal(err, out.String())
	}
	p, _ = board.Store{Home: home, Role: "cloud"}.Project(project)
	if p.Channels[0].Source != "Nebula Run · @Nebula Bot, #feedback" {
		t.Fatal(p.Channels[0].Source)
	}
	out.Reset()
	if err = discordCommand(ctx, home, []string{"places", "discord-ab12"}, &out); err != nil || !strings.Contains(out.String(), `"kind": "forum"`) || strings.Contains(out.String(), "Voice") {
		t.Fatal(err, out.String())
	}
	info, err := Info(home, "discord-ab12")
	if err != nil || !info.HasSecret || info.Discord.BotToken != "" || info.Discord.Forums[0] != "444444444444444444" {
		t.Fatalf("%+v %v", info, err)
	}
	// Same id from another project is refused.
	other := req("good-token")
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
	home, project := boardHome(t)
	os.MkdirAll(Dir(home), 0o700)
	os.WriteFile(ConfigPath(home), []byte(`{"public_url":"https://in.example.com/","channels":[]}`), 0o600)
	req := func(secret string, origins ...string) SetupRequest {
		return SetupRequest{Project: project, ID: "widget-cd34", Kind: KindWidget, Widget: &WidgetSetup{Origins: origins, SiteKey: "site", Secret: secret}}
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
