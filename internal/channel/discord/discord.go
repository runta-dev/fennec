// Package discord is the Discord channel of the external feedback intake
// (FEN-226, design in FEN-217). It works like a support bot in Slack:
//
//   - Someone @mentions the bot in a server channel: the bot starts a thread
//     on that message and files a request. Everything said later in the
//     thread joins that request as a follow-up.
//   - Every new post in a configured forum channel files a request; the
//     post's replies are its follow-ups.
//   - Reply answers in the thread and @mentions the author.
//
// All of it comes over the Discord Gateway (an outbound websocket
// authenticated by the bot token), so no public endpoint is needed.
package discord

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultAPIBase is Discord's REST API, version 10.
const DefaultAPIBase = "https://discord.com/api/v10"

// Config is one Discord bot connected to a project.
type Config struct {
	BotToken string   // "Bot ..." prefix is added when missing
	Guilds   []string // optional: only these servers
	Forums   []string // forum channel IDs whose new posts become requests
	// Channels, when set, are the only channels (and their threads) where
	// an @mention files a request. Empty: any channel the bot can read.
	Channels []string

	APIBase    string       // default DefaultAPIBase; tests point it at httptest
	GatewayURL string       // default: asked from GET /gateway/bot
	HTTPClient *http.Client // default: 15s timeout
}

// Adapter is the Discord channel. Create it with New.
type Adapter struct {
	cfg      Config
	guilds   map[string]bool
	forums   map[string]bool
	channels map[string]bool
	http     *http.Client

	mu    sync.Mutex
	botID string                 // from READY
	known map[string]channelInfo // channel and thread lookups
}

// New validates cfg and returns an Adapter.
func New(cfg Config) (*Adapter, error) {
	if strings.TrimSpace(strings.TrimPrefix(cfg.BotToken, "Bot ")) == "" {
		return nil, errors.New("discord: bot token is required")
	}
	for _, list := range [][]string{cfg.Guilds, cfg.Forums, cfg.Channels} {
		for _, id := range list {
			if !isSnowflake(strings.TrimSpace(id)) {
				return nil, fmt.Errorf("discord: %q is not a Discord ID", id)
			}
		}
	}
	if cfg.APIBase == "" {
		cfg.APIBase = DefaultAPIBase
	}
	cfg.APIBase = strings.TrimRight(cfg.APIBase, "/")
	if !strings.HasPrefix(cfg.BotToken, "Bot ") {
		cfg.BotToken = "Bot " + strings.TrimSpace(cfg.BotToken)
	}
	a := &Adapter{
		cfg:      cfg,
		guilds:   set(cfg.Guilds),
		forums:   set(cfg.Forums),
		channels: set(cfg.Channels),
		http:     cfg.HTTPClient,
		known:    map[string]channelInfo{},
	}
	if a.http == nil {
		a.http = &http.Client{Timeout: 15 * time.Second}
	}
	return a, nil
}

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
