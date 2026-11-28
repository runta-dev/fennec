package discord

import (
	"context"
	"errors"
	"strings"
)

// Application flags that allow reading message text over the Gateway
// (GATEWAY_MESSAGE_CONTENT and its limited variant for unverified bots).
const (
	flagMessageContent        = 1 << 18
	flagMessageContentLimited = 1 << 19
)

// CheckError says what is wrong in words Settings › Channels translates:
// Code is one of token, unreachable, application, public_key,
// message_content, guild, channel; Detail is the ID involved, if any.
type CheckError struct {
	Code   string
	Detail string
}

func (e *CheckError) Error() string { return "discord check: " + e.Code + " " + e.Detail }

// Info is what Check learned: names for people, for Settings › Channels.
type Info struct {
	Application string   // the application's name
	Guilds      []string // server names, in the order first seen
	Channels    []string // "#name" of each watched channel
}

// Source is the one-line description Settings › Channels shows, such as
// "Nebula Run · #bug-report, /feedback".
func (i Info) Source() string {
	parts := []string{}
	if len(i.Guilds) > 0 {
		parts = append(parts, strings.Join(i.Guilds, ", "))
	} else if i.Application != "" {
		parts = append(parts, i.Application)
	}
	parts = append(parts, strings.Join(append(append([]string{}, i.Channels...), "/feedback"), ", "))
	return strings.Join(parts, " · ")
}

// Check verifies the configuration against Discord before it is saved
// (FEN-228): the bot token works and belongs to the application, the public
// key is the application's, Message Content is enabled when channels are
// watched, and the bot can see every watched channel and server.
func (a *Adapter) Check(ctx context.Context) (Info, error) {
	var info Info
	var app struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		VerifyKey string `json:"verify_key"`
		Flags     int64  `json:"flags"`
	}
	if err := a.call(ctx, "GET", "/applications/@me", nil, &app); err != nil {
		if errors.Is(err, ErrPermanent) {
			return info, &CheckError{"token", ""}
		}
		return info, &CheckError{"unreachable", err.Error()}
	}
	info.Application = app.Name
	if app.ID != a.cfg.ApplicationID {
		return info, &CheckError{"application", app.ID}
	}
	if !strings.EqualFold(app.VerifyKey, strings.TrimSpace(a.cfg.PublicKey)) {
		return info, &CheckError{"public_key", ""}
	}
	if len(a.cfg.Channels) > 0 && app.Flags&(flagMessageContent|flagMessageContentLimited) == 0 {
		return info, &CheckError{"message_content", ""}
	}
	seen := map[string]bool{}
	guild := func(id string) error {
		if id == "" || seen[id] {
			return nil
		}
		seen[id] = true
		var g struct {
			Name string `json:"name"`
		}
		if err := a.call(ctx, "GET", "/guilds/"+id, nil, &g); err != nil {
			return &CheckError{"guild", id}
		}
		info.Guilds = append(info.Guilds, g.Name)
		return nil
	}
	for _, id := range a.cfg.Guilds {
		if err := guild(id); err != nil {
			return info, err
		}
	}
	for _, id := range a.cfg.Channels {
		var ch struct {
			Name    string `json:"name"`
			GuildID string `json:"guild_id"`
		}
		if err := a.call(ctx, "GET", "/channels/"+id, nil, &ch); err != nil {
			return info, &CheckError{"channel", id}
		}
		info.Channels = append(info.Channels, "#"+ch.Name)
		if err := guild(ch.GuildID); err != nil {
			return info, err
		}
	}
	return info, nil
}
