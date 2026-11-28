package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"fennec/internal/board"
	"fennec/internal/channel/discord"
)

// SetupRequest is what Settings › Channels sends to connect or update a
// channel (FEN-228): the Mac pipes it over SSH to `fennec-terminal channel
// setup`, so the secrets only ever land in the cloud config file. An empty
// secret keeps the one already saved, so editing doesn't require pasting it
// again.
type SetupRequest struct {
	Project string        `json:"project"`
	ID      string        `json:"id"`
	Kind    string        `json:"kind"`
	Discord *DiscordSetup `json:"discord,omitempty"`
	Widget  *WidgetSetup  `json:"widget,omitempty"`
}

// DiscordSetup is the bot token, which is all a person has to give: the
// application, servers and channels are read from Discord with it. Forums,
// Channels and Guilds narrow where the bot listens; the agent fills them in
// after asking (see `channel discord ...`).
type DiscordSetup struct {
	BotToken string   `json:"bot_token,omitempty"`
	Forums   []string `json:"forums,omitempty"`
	Channels []string `json:"channels,omitempty"`
	Guilds   []string `json:"guilds,omitempty"`
}

type WidgetSetup struct {
	Origins []string `json:"origins"`
	SiteKey string   `json:"site_key"`
	Secret  string   `json:"secret,omitempty"`
}

// SetupResult is printed as JSON. On failure Code says what is wrong, in a
// word Settings › Channels translates (setup_error_<code>), and Detail names
// the ID or value involved.
type SetupResult struct {
	OK     bool   `json:"ok"`
	Source string `json:"source,omitempty"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
	// The widget's embed tag. Empty while the config has no public_url.
	Snippet string `json:"snippet,omitempty"`
	// Discord: what the bot can see now, and its invite link.
	Discord *discord.Status `json:"discord,omitempty"`
}

// ChannelInfo is the saved, non-secret part of a channel, for the form.
type ChannelInfo struct {
	Kind    string        `json:"kind"`
	Discord *DiscordSetup `json:"discord,omitempty"`
	Widget  *WidgetSetup  `json:"widget,omitempty"`
	// Secrets reports which secrets are saved, never their values.
	HasSecret bool `json:"has_secret"`
}

// DiscordAPIBase is overridden by tests.
var DiscordAPIBase = discord.DefaultAPIBase

func failed(code, detail string) SetupResult { return SetupResult{Code: code, Detail: detail} }

func trimAll(list []string) []string {
	out := []string{}
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// Setup checks the request against the service it names and, if it works,
// saves it. A nil error with OK false is the user's to fix.
func Setup(ctx context.Context, home string, req SetupRequest) (SetupResult, error) {
	if !channelID.MatchString(req.ID) || req.Project == "" {
		return failed("invalid", ""), nil
	}
	cfg, err := LoadConfig(home)
	if errors.Is(err, os.ErrNotExist) {
		cfg, err = Config{}, nil
	}
	if err != nil {
		return SetupResult{}, err
	}
	old, had := cfg.Spec(req.ID)
	if had && (old.Kind != req.Kind || old.Project != req.Project) {
		return failed("taken", req.ID), nil
	}
	spec := old
	spec.ID, spec.Kind, spec.Project = req.ID, req.Kind, req.Project
	var result SetupResult
	switch req.Kind {
	case KindDiscord:
		d := req.Discord
		if d == nil {
			return failed("invalid", ""), nil
		}
		token := strings.TrimSpace(d.BotToken)
		if token == "" && old.Discord != nil {
			token = old.Discord.BotToken
		}
		// A list left out (nil) keeps what is saved: replacing the token
		// in Settings must not forget the forum the agent set up.
		keep := func(list []string, saved func(*DiscordSpec) []string) []string {
			if list == nil && old.Discord != nil {
				return saved(old.Discord)
			}
			return trimAll(list)
		}
		spec.Discord = &DiscordSpec{
			BotToken: token,
			Forums:   keep(d.Forums, func(o *DiscordSpec) []string { return o.Forums }),
			Channels: keep(d.Channels, func(o *DiscordSpec) []string { return o.Channels }),
			Guilds:   keep(d.Guilds, func(o *DiscordSpec) []string { return o.Guilds }),
		}
		st, err := checkDiscord(ctx, spec.Discord)
		var bad *discord.CheckError
		if errors.As(err, &bad) {
			return SetupResult{Code: "discord_" + bad.Code, Detail: bad.Detail, Discord: st}, nil
		}
		if err != nil {
			return SetupResult{}, err
		}
		result.Source, result.Discord = st.Source(), st
	case KindWidget:
		w := req.Widget
		if w == nil {
			return failed("invalid", ""), nil
		}
		secret := strings.TrimSpace(w.Secret)
		if secret == "" && old.Turnstile != nil {
			secret = old.Turnstile.Secret
		}
		spec.Origins = trimAll(w.Origins)
		spec.Turnstile = &Turnstile{SiteKey: strings.TrimSpace(w.SiteKey), Secret: secret}
		if len(spec.Origins) == 0 {
			return failed("widget_origins", ""), nil
		}
		hosts := []string{}
		for _, o := range spec.Origins {
			if _, ok := parseOrigin(o, true); !ok {
				return failed("widget_origin", o), nil
			}
			hosts = append(hosts, strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://"))
		}
		if spec.Turnstile.SiteKey == "" || secret == "" {
			return failed("widget_turnstile", ""), nil
		}
		if code, err := checkTurnstileSecret(ctx, secret); err != nil {
			return failed("widget_unreachable", err.Error()), nil
		} else if code != "" {
			return failed(code, ""), nil
		}
		result.Source = strings.Join(hosts, " · ")
		if base := strings.TrimRight(cfg.PublicURL, "/"); base != "" {
			result.Snippet = fmt.Sprintf("<script src=%q data-channel=%q async></script>", base+"/widget.js", spec.ID)
		}
	default:
		return failed("kind", req.Kind), nil
	}
	if had {
		for i := range cfg.Channels {
			if cfg.Channels[i].ID == spec.ID {
				cfg.Channels[i] = spec
			}
		}
	} else {
		cfg.Channels = append(cfg.Channels, spec)
	}
	if err := cfg.Validate(); err != nil {
		return failed("invalid", err.Error()), nil
	}
	if err := WriteConfig(home, cfg); err != nil {
		return SetupResult{}, err
	}
	if err := upsertProjectChannel(home, spec, result.Source); err != nil {
		return SetupResult{}, err
	}
	result.OK = true
	return result, nil
}

// checkDiscord reads the bot's state and, when Message Content is off,
// switches it on through the API and looks again.
func checkDiscord(ctx context.Context, spec *DiscordSpec) (*discord.Status, error) {
	c := spec.config()
	c.APIBase = DiscordAPIBase
	adapter, err := discord.New(c)
	if err != nil {
		return nil, &discord.CheckError{Code: "format", Detail: err.Error()}
	}
	st, err := adapter.Check(ctx)
	var bad *discord.CheckError
	if errors.As(err, &bad) && bad.Code == "message_content" {
		if err = adapter.EnableMessageContent(ctx); err == nil {
			st, err = adapter.Check(ctx)
		}
	}
	return &st, err
}

// upsertProjectChannel keeps the project's row in Settings › Channels in
// step with the config: a new channel gets the default tags and is on; an
// existing one gets the new source. The board replicates this to the Macs;
// it holds no secret.
func upsertProjectChannel(home string, spec ChannelSpec, source string) error {
	s := board.Store{Home: home, Role: "cloud"}
	p, err := s.Project(spec.Project)
	if err != nil {
		return err
	}
	channels := append([]board.Channel(nil), p.Channels...)
	tags := append([]board.Tag(nil), p.Tags...)
	found := false
	for i := range channels {
		if channels[i].ID == spec.ID {
			channels[i].Source, found = source, true
		}
	}
	if !found {
		ch := board.Channel{ID: spec.ID, Kind: spec.Kind, Source: source, On: true}
		own := map[string]string{"discord": "Discord", "widget": "Widget"}[spec.Kind]
		for _, want := range []board.Tag{{ID: "feedback", Label: "Feedback"}, {ID: spec.Kind, Label: own}} {
			id := want.ID
			if i := slices.IndexFunc(tags, func(t board.Tag) bool { return t.ID == want.ID || strings.EqualFold(t.Label, want.Label) }); i >= 0 {
				id = tags[i].ID
			} else {
				tags = append(tags, want)
			}
			ch.Tags = append(ch.Tags, id)
		}
		channels = append(channels, ch)
	}
	change, err := json.Marshal(map[string]any{"channels": channels, "tags": tags})
	if err != nil {
		return err
	}
	_, err = s.UpdateProject(spec.Project, change)
	return err
}

// checkTurnstileSecret asks Cloudflare about a dummy token: a wrong secret
// is reported as invalid-input-secret before the token is even looked at.
func checkTurnstileSecret(ctx context.Context, secret string) (string, error) {
	if secret == turnstileTestSecret {
		return "", nil
	}
	form := url.Values{"secret": {secret}, "response": {"fennec-settings-check"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TurnstileVerifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var answer struct {
		Errors []string `json:"error-codes"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&answer); err != nil {
		return "", err
	}
	if slices.Contains(answer.Errors, "invalid-input-secret") || slices.Contains(answer.Errors, "missing-input-secret") {
		return "widget_secret", nil
	}
	return "", nil
}

// Info returns a saved channel without its secrets.
func Info(home, id string) (ChannelInfo, error) {
	cfg, err := LoadConfig(home)
	if errors.Is(err, os.ErrNotExist) {
		return ChannelInfo{}, nil
	}
	if err != nil {
		return ChannelInfo{}, err
	}
	spec, ok := cfg.Spec(id)
	if !ok {
		return ChannelInfo{}, nil
	}
	info := ChannelInfo{Kind: spec.Kind}
	if d := spec.Discord; d != nil {
		info.Discord = &DiscordSetup{Forums: d.Forums, Channels: d.Channels, Guilds: d.Guilds}
		info.HasSecret = d.BotToken != ""
	}
	if spec.Turnstile != nil {
		info.Widget = &WidgetSetup{Origins: spec.Origins, SiteKey: spec.Turnstile.SiteKey}
		info.HasSecret = spec.Turnstile.Secret != ""
	}
	return info, nil
}

// Remove deletes a channel and its secrets from the config.
func Remove(home, id string) error {
	cfg, err := LoadConfig(home)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cfg.Channels = slices.DeleteFunc(cfg.Channels, func(ch ChannelSpec) bool { return ch.ID == id })
	return WriteConfig(home, cfg)
}
