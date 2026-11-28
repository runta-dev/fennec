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

type DiscordSetup struct {
	ApplicationID string   `json:"application_id"`
	PublicKey     string   `json:"public_key"`
	BotToken      string   `json:"bot_token,omitempty"`
	Channels      []string `json:"channels,omitempty"`
	Guilds        []string `json:"guilds,omitempty"`
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
	// Where to point the service: Discord's Interactions Endpoint URL, or
	// the widget's embed tag. Empty while the config has no public_url.
	InteractionsURL string `json:"interactions_url,omitempty"`
	Snippet         string `json:"snippet,omitempty"`
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
		spec.Discord = &DiscordSpec{
			ApplicationID: strings.TrimSpace(d.ApplicationID),
			PublicKey:     strings.ToLower(strings.TrimSpace(d.PublicKey)),
			BotToken:      token,
			Channels:      trimAll(d.Channels),
			Guilds:        trimAll(d.Guilds),
		}
		c := spec.Discord.config()
		c.APIBase = DiscordAPIBase
		adapter, err := discord.New(c)
		if err != nil {
			return failed("discord_format", err.Error()), nil
		}
		info, err := adapter.Check(ctx)
		var bad *discord.CheckError
		if errors.As(err, &bad) {
			return failed("discord_"+bad.Code, bad.Detail), nil
		}
		if err != nil {
			return SetupResult{}, err
		}
		result.Source = info.Source()
		if base := strings.TrimRight(cfg.PublicURL, "/"); base != "" {
			result.InteractionsURL = base + "/v1/discord/" + spec.ID + "/interactions"
		}
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
	result.OK = true
	return result, nil
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
		info.Discord = &DiscordSetup{ApplicationID: d.ApplicationID, PublicKey: d.PublicKey, Channels: d.Channels, Guilds: d.Guilds}
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
