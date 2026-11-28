package channel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"fennec/internal/channel/discord"
)

// KindDiscord is a Discord application (FEN-226): its /feedback command
// reaches POST /v1/discord/{id}/interactions, and messages in its watched
// channels come in over the Discord Gateway, which `channel serve` holds open.
const KindDiscord = "discord"

// DiscordSpec is a discord channel's application, from the Discord
// Developer Portal. The bot needs the Message Content intent and, in the
// watched channels, View Channel, Send Messages, Read Message History and
// Add Reactions.
type DiscordSpec struct {
	ApplicationID string `json:"application_id"`
	PublicKey     string `json:"public_key"` // hex ed25519, checked on every interaction
	BotToken      string `json:"bot_token"`
	// Channels are the Discord channel IDs whose messages become feedback.
	// Empty: only /feedback.
	Channels []string `json:"channels,omitempty"`
	// Guilds, when set, are the only servers /feedback is taken from; the
	// command is then registered per server (instant) instead of globally.
	Guilds []string `json:"guilds,omitempty"`
}

func (d *DiscordSpec) config() discord.Config {
	return discord.Config{ApplicationID: d.ApplicationID, PublicKey: d.PublicKey, BotToken: d.BotToken, Channels: d.Channels, Guilds: d.Guilds}
}

func validateDiscord(ch ChannelSpec) error {
	if ch.Discord == nil {
		return fmt.Errorf("channel %s: a discord channel needs discord.application_id, public_key and bot_token", ch.ID)
	}
	if _, err := discord.New(ch.Discord.config()); err != nil {
		return fmt.Errorf("channel %s: %w", ch.ID, err)
	}
	return nil
}

// newDiscord builds an adapter; tests point it at a fake Discord API.
var newDiscord = discord.New

// discordChannel is a Discord adapter as a Channel. Ingest isn't used:
// Discord traffic arrives through the interactions route and the Gateway.
type discordChannel struct{ a *discord.Adapter }

func (discordChannel) Ingest(*http.Request) (Feedback, error) {
	return Feedback{}, errors.New("discord feedback arrives through the interactions route and the gateway")
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

// discordSink files what the adapter normalized, like a widget submission.
func discordSink(home string, spec ChannelSpec, logf func(string, ...any)) discord.Sink {
	return discord.SinkFunc(func(_ context.Context, f discord.Feedback) (int, error) {
		fb := fromDiscord(spec, f)
		req, err := Deliver(home, spec, fb)
		if err != nil {
			return 0, err
		}
		logf("channel %s: filed request %d in %s", spec.ID, req.Number, spec.Project)
		return req.Number, nil
	})
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
	return Feedback{Channel: spec.ID, Kind: KindDiscord, Author: oneLine(name, 80), Text: f.Text, Context: ctx, ReplyRef: f.ReplyRef, Received: time.Now().UTC()}
}

// discordInteractions serves one discord channel's Interactions Endpoint
// URL. The config is read per request, like the widget routes, and a channel
// switched off answers 404 before the signature is looked at.
func (s Server) discordInteractions(w http.ResponseWriter, r *http.Request) {
	cfg, err := LoadConfig(s.Home)
	if err != nil {
		s.logf("config: %v", err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	spec, ok := cfg.Spec(r.PathValue("id"))
	if !ok || spec.Kind != KindDiscord || !s.on(spec) {
		http.NotFound(w, r)
		return
	}
	a, err := newDiscord(spec.Discord.config())
	if err != nil {
		s.logf("channel %s: %v", spec.ID, err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	a.InteractionsHandler(discordSink(s.Home, spec, s.logf)).ServeHTTP(w, r)
}

// runDiscord registers /feedback and holds the Gateway for every enabled
// discord channel until ctx ends. Gateway settings apply on restart.
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
			if err := a.RegisterCommands(ctx); err != nil {
				s.logf("channel %s: registering /feedback: %v", spec.ID, err)
			}
		}()
		go func() {
			// The board's on/off switch is checked per message by Deliver.
			sink := discordSink(s.Home, spec, s.logf)
			if err := a.Run(ctx, sink); err != nil && !errors.Is(err, context.Canceled) {
				s.logf("channel %s: gateway stopped: %v", spec.ID, err)
			}
		}()
	}
}
