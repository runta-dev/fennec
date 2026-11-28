package channel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"fennec/internal/board"
	"fennec/internal/channel/discord"
)

// KindDiscord is a Discord bot (FEN-226). Like a support bot in Slack, it
// starts a thread when someone @mentions it and files a request for every new
// post in its forums; messages later in that thread or post are follow-ups.
// It runs entirely over the Discord Gateway, which `channel serve` holds open,
// so it needs no public URL.
const KindDiscord = "discord"

// DiscordSpec is a discord channel's bot, from the Discord Developer Portal.
// The bot needs the Message Content intent, and View Channel, Send Messages,
// Send Messages in Threads, Create Public Threads and Read Message History.
type DiscordSpec struct {
	BotToken string `json:"bot_token"`
	// Forums are forum channel IDs: every new post in one is a request.
	Forums []string `json:"forums,omitempty"`
	// Channels, when set, are the only channels where an @mention files a
	// request. Empty: every channel the bot can read.
	Channels []string `json:"channels,omitempty"`
	// Guilds, when set, are the only servers the bot answers in.
	Guilds []string `json:"guilds,omitempty"`
}

func (d *DiscordSpec) config() discord.Config {
	return discord.Config{BotToken: d.BotToken, Forums: d.Forums, Channels: d.Channels, Guilds: d.Guilds}
}

func validateDiscord(ch ChannelSpec) error {
	if ch.Discord == nil {
		return fmt.Errorf("channel %s: a discord channel needs discord.bot_token", ch.ID)
	}
	if _, err := discord.New(ch.Discord.config()); err != nil {
		return fmt.Errorf("channel %s: %w", ch.ID, err)
	}
	return nil
}

// newDiscord builds an adapter; tests point it at a fake Discord API.
var newDiscord = discord.New

// discordChannel is a Discord adapter as a Channel. Ingest isn't used:
// Discord traffic arrives over the Gateway.
type discordChannel struct{ a *discord.Adapter }

func (discordChannel) Ingest(*http.Request) (Feedback, error) {
	return Feedback{}, errors.New("discord feedback arrives over the gateway")
}

func (d discordChannel) Reply(ctx context.Context, ref, text string) error {
	return d.a.Reply(ctx, ref, text)
}

func openDiscord(spec ChannelSpec) (Channel, error) {
	if spec.Discord == nil {
		return nil, fmt.Errorf("channel %s: no discord settings", spec.ID)
	}
	a, err := newDiscord(spec.Discord.config())
	if err != nil {
		return nil, err
	}
	return discordChannel{a}, nil
}

// discordSink files what the adapter normalized.
type discordSink struct {
	home string
	spec ChannelSpec
	logf func(string, ...any)
}

func (d discordSink) Submit(_ context.Context, f discord.Feedback) (int, error) {
	req, err := Deliver(d.home, d.spec, fromDiscord(d.spec, f))
	if err != nil {
		return 0, err
	}
	d.logf("channel %s: filed request %d in %s", d.spec.ID, req.Number, d.spec.Project)
	return req.Number, nil
}

func (d discordSink) FollowUp(_ context.Context, f discord.Feedback) (int, bool, error) {
	req, ok, err := FollowUp(d.home, d.spec, fromDiscord(d.spec, f))
	if ok && err == nil {
		d.logf("channel %s: follow-up on request %d in %s", d.spec.ID, req.Number, d.spec.Project)
	}
	return req.Number, ok, err
}

// Active: the request (or the open one it was merged into) is Queued or
// Working, so the bot keeps showing that it is typing (FEN-242).
func (d discordSink) Active(_ context.Context, n int) bool {
	s := board.Store{Home: d.home, Role: "cloud"}
	for range 20 {
		r, err := s.Request(d.spec.Project, n)
		if err != nil {
			return false
		}
		if dup := r.DuplicateOf(); dup != 0 {
			n = dup
			continue
		}
		return r.Status == board.Queued || r.Status == board.Working
	}
	return false
}

func fromDiscord(spec ChannelSpec, f discord.Feedback) Feedback {
	ctx := map[string]string{}
	for k, v := range f.Context {
		if v != "" {
			ctx[k] = v
		}
	}
	ctx["discord_user"] = f.Author.ID
	if f.Author.Handle != "" {
		ctx["discord_handle"] = "@" + f.Author.Handle
	}
	name := f.Author.Name
	if name == "" {
		name = f.Author.Handle
	}
	return Feedback{Channel: spec.ID, Kind: KindDiscord, Author: oneLine(name, 80), Title: f.Title, Text: f.Text, Context: ctx,
		Conversation: f.Conversation, ReplyRef: f.ReplyRef, Received: time.Now().UTC()}
}

// runDiscord holds the Gateway for every enabled discord channel until ctx
// ends. Discord settings apply on restart; the Settings › Channels switch is
// checked per message.
func (s Server) runDiscord(ctx context.Context, cfg Config) {
	for _, spec := range cfg.Channels {
		if spec.Kind != KindDiscord || spec.Disabled {
			continue
		}
		a, err := newDiscord(spec.Discord.config())
		if err != nil {
			s.logf("channel %s: %v", spec.ID, err)
			continue
		}
		go func() {
			sink := discordSink{home: s.Home, spec: spec, logf: s.logf}
			if err := a.Run(ctx, sink); err != nil && !errors.Is(err, context.Canceled) {
				s.logf("channel %s: gateway stopped: %v", spec.ID, err)
			}
		}()
	}
}
