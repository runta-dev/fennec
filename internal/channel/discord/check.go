package discord

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Application flags that allow reading message text over the Gateway.
// Only the limited one (bots in fewer than 100 servers) can be set through
// the API; the other is granted by Discord after verification.
const (
	flagMessageContent        = 1 << 18
	flagMessageContentLimited = 1 << 19
)

// InvitePermissions are what the bot needs in a server: View Channel, Send
// Messages, Send Messages in Threads, Create Public Threads, Read Message
// History.
const InvitePermissions = 1<<10 | 1<<11 | 1<<38 | 1<<35 | 1<<16

// CheckError says what is wrong in words Settings › Channels translates:
// Code is one of token, unreachable, message_content, guild, channel,
// not_forum; Detail is the ID involved, if any.
type CheckError struct {
	Code   string
	Detail string
}

func (e *CheckError) Error() string { return "discord check: " + e.Code + " " + e.Detail }

// Place is a server or a channel, by ID and name.
type Place struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind is "text", "forum" or "announcement" for channels; empty for servers.
	Kind  string `json:"kind,omitempty"`
	Guild string `json:"guild,omitempty"`
}

// Status is everything the setup needs to know about the bot (FEN-228).
type Status struct {
	Application   string  `json:"application"`
	ApplicationID string  `json:"application_id"`
	Bot           string  `json:"bot"`
	InviteURL     string  `json:"invite_url"`
	MessageIntent bool    `json:"message_content_intent"`
	Guilds        []Place `json:"guilds"`
	Forums        []Place `json:"forums"`
	Channels      []Place `json:"channels"`
}

// Source is the one-line description Settings › Channels shows, such as
// "Nebula Run · @Nebula Bot, forum #feedback".
func (s Status) Source() string {
	names := func(list []Place) []string {
		out := []string{}
		for _, p := range list {
			out = append(out, p.Name)
		}
		return out
	}
	parts := []string{}
	if len(s.Guilds) > 0 {
		parts = append(parts, strings.Join(names(s.Guilds), ", "))
	}
	entries := []string{"@" + s.Bot}
	for _, f := range s.Forums {
		entries = append(entries, "#"+f.Name)
	}
	parts = append(parts, strings.Join(entries, ", "))
	return strings.Join(parts, " · ")
}

func (a *Adapter) wrap(err error) error {
	if errors.Is(err, ErrPermanent) {
		return &CheckError{"token", ""}
	}
	return &CheckError{"unreachable", err.Error()}
}

// Check reads the bot's state from Discord with nothing but the token: the
// application behind it, the servers it is in, whether it may read message
// text, and that every configured forum, channel and server is visible. An
// error is a *CheckError.
func (a *Adapter) Check(ctx context.Context) (Status, error) {
	var st Status
	var app struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Flags int64  `json:"flags"`
		Bot   struct {
			Username string `json:"username"`
		} `json:"bot"`
	}
	if err := a.call(ctx, "GET", "/applications/@me", nil, &app); err != nil {
		return st, a.wrap(err)
	}
	st.Application, st.ApplicationID, st.Bot = app.Name, app.ID, app.Bot.Username
	if st.Bot == "" {
		st.Bot = app.Name
	}
	st.MessageIntent = app.Flags&(flagMessageContent|flagMessageContentLimited) != 0
	st.InviteURL = "https://discord.com/oauth2/authorize?" + url.Values{
		"client_id":   {app.ID},
		"scope":       {"bot"},
		"permissions": {fmt.Sprint(InvitePermissions)},
	}.Encode()
	var guilds []Place
	if err := a.call(ctx, "GET", "/users/@me/guilds", nil, &guilds); err != nil {
		return st, a.wrap(err)
	}
	in := map[string]bool{}
	for _, g := range guilds {
		in[g.ID] = true
		if len(a.cfg.Guilds) == 0 || slices.Contains(a.cfg.Guilds, g.ID) {
			st.Guilds = append(st.Guilds, Place{ID: g.ID, Name: g.Name})
		}
	}
	for _, id := range a.cfg.Guilds {
		if !in[id] {
			return st, &CheckError{"guild", id}
		}
	}
	for _, list := range []struct {
		ids   []string
		forum bool
	}{{a.cfg.Forums, true}, {a.cfg.Channels, false}} {
		for _, id := range list.ids {
			var ch struct {
				Name    string `json:"name"`
				Type    int    `json:"type"`
				GuildID string `json:"guild_id"`
			}
			if err := a.call(ctx, "GET", "/channels/"+id, nil, &ch); err != nil {
				return st, &CheckError{"channel", id}
			}
			place := Place{ID: id, Name: ch.Name, Kind: channelKind(ch.Type), Guild: ch.GuildID}
			if list.forum {
				if ch.Type != typeForum {
					return st, &CheckError{"not_forum", id}
				}
				st.Forums = append(st.Forums, place)
			} else {
				st.Channels = append(st.Channels, place)
			}
		}
	}
	if !st.MessageIntent {
		return st, &CheckError{"message_content", ""}
	}
	return st, nil
}

// Channel types (Discord API).
const (
	typeText         = 0
	typeAnnouncement = 5
	typeForum        = 15
)

func channelKind(t int) string {
	switch t {
	case typeText:
		return "text"
	case typeAnnouncement:
		return "announcement"
	case typeForum:
		return "forum"
	}
	return ""
}

// Places lists the text, announcement and forum channels of every server
// the bot is in, for picking which to use.
func (a *Adapter) Places(ctx context.Context) ([]Place, error) {
	var guilds []Place
	if err := a.call(ctx, "GET", "/users/@me/guilds", nil, &guilds); err != nil {
		return nil, a.wrap(err)
	}
	out := []Place{}
	for _, g := range guilds {
		var channels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type int    `json:"type"`
		}
		if err := a.call(ctx, "GET", "/guilds/"+g.ID+"/channels", nil, &channels); err != nil {
			return nil, a.wrap(err)
		}
		for _, ch := range channels {
			if kind := channelKind(ch.Type); kind != "" {
				out = append(out, Place{ID: ch.ID, Name: ch.Name, Kind: kind, Guild: g.Name})
			}
		}
	}
	return out, nil
}

// EnableMessageContent turns on the (limited) Message Content intent, the
// one portal switch the API can flip, so nobody has to find it by hand.
func (a *Adapter) EnableMessageContent(ctx context.Context) error {
	var app struct {
		Flags int64 `json:"flags"`
	}
	if err := a.call(ctx, "GET", "/applications/@me", nil, &app); err != nil {
		return a.wrap(err)
	}
	if app.Flags&(flagMessageContent|flagMessageContentLimited) != 0 {
		return nil
	}
	// Only the limited intent flags are writable; send just those.
	const writable = 1<<13 | 1<<15 | flagMessageContentLimited
	flags := app.Flags&writable | flagMessageContentLimited
	if err := a.call(ctx, "PATCH", "/applications/@me", map[string]int64{"flags": flags}, nil); err != nil {
		return a.wrap(err)
	}
	return nil
}
