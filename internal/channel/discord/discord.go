// Package discord is the Discord channel of the external feedback intake
// (FEN-226, design in FEN-217).
//
// Two inbound paths, one outbound:
//
//   - /feedback slash command, delivered by Discord to the interactions
//     endpoint (HTTP). Every request is checked against the application's
//     ed25519 public key before anything else is read.
//   - Ordinary messages in the watched channels, read from the Discord
//     Gateway (websocket, GUILD_MESSAGES + MESSAGE_CONTENT intents).
//   - Reply posts in the original channel, mentions the author and, for a
//     channel message, replies to that message.
package discord

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ChannelName is the value of Feedback.Channel for this adapter.
const ChannelName = "discord"

// DefaultAPIBase is Discord's REST API, version 10.
const DefaultAPIBase = "https://discord.com/api/v10"

// Config is one Discord application/bot connected to a project.
type Config struct {
	ApplicationID string   // application (client) ID
	PublicKey     string   // hex ed25519 key from the Developer Portal
	BotToken      string   // "Bot ..." prefix is added when missing
	Channels      []string // channel IDs whose messages become feedback
	Guilds        []string // optional: only accept /feedback from these servers
	CommandName   string   // default "feedback"

	APIBase    string       // default DefaultAPIBase; tests point it at httptest
	GatewayURL string       // default: asked from GET /gateway/bot
	HTTPClient *http.Client // default: 15s timeout
}

// Adapter is the Discord channel. Create it with New.
type Adapter struct {
	cfg       Config
	publicKey ed25519.PublicKey
	channels  map[string]bool
	guilds    map[string]bool
	http      *http.Client
	now       func() time.Time
}

// New validates cfg and returns an Adapter.
func New(cfg Config) (*Adapter, error) {
	key, err := hex.DecodeString(strings.TrimSpace(cfg.PublicKey))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("discord: public key must be 64 hex characters")
	}
	if strings.TrimSpace(cfg.BotToken) == "" {
		return nil, errors.New("discord: bot token is required")
	}
	if !isSnowflake(cfg.ApplicationID) {
		return nil, errors.New("discord: application ID is required")
	}
	if cfg.CommandName == "" {
		cfg.CommandName = "feedback"
	}
	if cfg.APIBase == "" {
		cfg.APIBase = DefaultAPIBase
	}
	cfg.APIBase = strings.TrimRight(cfg.APIBase, "/")
	if !strings.HasPrefix(cfg.BotToken, "Bot ") {
		cfg.BotToken = "Bot " + strings.TrimSpace(cfg.BotToken)
	}
	a := &Adapter{
		cfg:       cfg,
		publicKey: ed25519.PublicKey(key),
		channels:  set(cfg.Channels),
		guilds:    set(cfg.Guilds),
		http:      cfg.HTTPClient,
		now:       time.Now,
	}
	if a.http == nil {
		a.http = &http.Client{Timeout: 15 * time.Second}
	}
	for id := range a.channels {
		if !isSnowflake(id) {
			return nil, fmt.Errorf("discord: channel %q is not a Discord ID", id)
		}
	}
	return a, nil
}

// Name returns the channel name used in Feedback.Channel and tags.
func (a *Adapter) Name() string { return ChannelName }

// Watches reports whether messages in channelID become feedback.
func (a *Adapter) Watches(channelID string) bool { return a.channels[channelID] }

func set(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			m[id] = true
		}
	}
	return m
}

// isSnowflake reports whether s looks like a Discord ID (decimal digits).
func isSnowflake(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
