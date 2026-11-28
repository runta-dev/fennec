// Package channel is the public intake for external feedback (FEN-224, from
// the FEN-217 design). A channel is an inbound source such as the web
// widget or Discord. Each one implements two functions:
//
//	Ingest(payload) -> Feedback   // verify the sender, normalize
//	Reply(reply_ref, text)        // answer the original author
//
// Every Feedback becomes a new request in the channel's project, tagged with
// the channel's tags; there is no triage step and no rate limit. The author's
// text is untrusted: it reaches the agent only as quoted data (see Body).
//
// The board's HTTP API has no authentication and must never be exposed, so
// the intake runs as its own server (`fennec-terminal channel serve`) that
// serves only the submission routes and writes to the board store directly.
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Feedback is one message from an external user, normalized by a channel.
type Feedback struct {
	// Channel is the configured channel's id, Kind its type ("widget").
	Channel string `json:"channel"`
	Kind    string `json:"kind"`
	// Author is the display name the user gave, or a generic one.
	Author string `json:"author"`
	Text   string `json:"text"`
	// Context is what the channel knows about where it was sent from, such
	// as the page URL or the browser. Values are untrusted, like Text.
	Context map[string]string `json:"context,omitempty"`
	// ReplyRef is the channel's own, opaque way to reach the author again
	// (for the widget, the email address). Empty: the author can't be
	// answered. It stays in the channel store and never reaches the board.
	ReplyRef string    `json:"reply_ref,omitempty"`
	Received time.Time `json:"received"`
	// Title, if set, is the request title the author gave (a forum post's).
	Title string `json:"title,omitempty"`
	// Conversation is the channel's id for the exchange the feedback opened,
	// such as a Discord thread. Later messages in it are follow-ups of the
	// same request (see FollowUp).
	Conversation string `json:"conversation,omitempty"`
}

// Channel is one inbound source.
type Channel interface {
	// Ingest verifies a submission and normalizes it. An error wrapping
	// ErrRejected is the sender's fault and answered 4xx.
	Ingest(r *http.Request) (Feedback, error)
	// Reply sends text to the author behind ref.
	Reply(ctx context.Context, ref, text string) error
}

// ErrRejected marks a submission refused for the sender's fault (bad origin,
// failed challenge, invalid body).
var ErrRejected = errors.New("rejected")

func rejected(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRejected, fmt.Sprintf(format, args...))
}

// Config is ~/.fen/channels/config.json on the cloud computer. It holds
// secrets (Turnstile, SMTP), so it is kept out of the board, which replicates
// to Macs, and is written with mode 0600.
type Config struct {
	// Listen is the server's address, e.g. "127.0.0.1:8787". Put a TLS
	// proxy or tunnel in front of it for the public URL.
	Listen string `json:"listen,omitempty"`
	// PublicURL is where browsers reach the server; the embed snippet uses it.
	PublicURL string        `json:"public_url,omitempty"`
	SMTP      *SMTP         `json:"smtp,omitempty"`
	Channels  []ChannelSpec `json:"channels"`
}

// ChannelSpec configures one channel.
type ChannelSpec struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Project string `json:"project"`
	// ID matches the project's channel in Settings › Channels (board
	// Project.Channels, FEN-228), whose tags and on/off switch apply. Without
	// one, Tags (ids or labels; missing ones are added to the project) are
	// given on top of Feedback and the kind's tag.
	Tags     []string `json:"tags,omitempty"`
	Disabled bool     `json:"disabled,omitempty"`
	// Widget: the pages allowed to embed it ("https://example.com",
	// "https://*.example.com"), and its Turnstile keys.
	Origins   []string   `json:"origins,omitempty"`
	Turnstile *Turnstile `json:"turnstile,omitempty"`
	// Discord: the application and bot (FEN-226).
	Discord *DiscordSpec `json:"discord,omitempty"`
}

// Turnstile holds a Cloudflare Turnstile widget's keys.
type Turnstile struct {
	SiteKey string `json:"site_key"`
	Secret  string `json:"secret"`
}

// SMTP is the mail server replies to widget users go through.
type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// From is the sender, e.g. "Fennec <feedback@example.com>".
	From string `json:"from"`
}

var channelID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Dir is where the intake keeps its config and reply refs.
func Dir(home string) string { return filepath.Join(home, ".fen", "channels") }

// ConfigPath is the config file.
func ConfigPath(home string) string { return filepath.Join(Dir(home), "config.json") }

// LoadConfig reads and checks the config.
func LoadConfig(home string) (Config, error) {
	var c Config
	data, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %w", ConfigPath(home), err)
	}
	return c, c.Validate()
}

// Validate reports the first problem in the config.
func (c Config) Validate() error {
	seen := map[string]bool{}
	for _, ch := range c.Channels {
		if !channelID.MatchString(ch.ID) || seen[ch.ID] {
			return fmt.Errorf("channel id %q: must be unique, lowercase letters, digits and -", ch.ID)
		}
		seen[ch.ID] = true
		if ch.Project == "" {
			return fmt.Errorf("channel %s: no project", ch.ID)
		}
		switch ch.Kind {
		case KindWidget:
			if len(ch.Origins) == 0 {
				return fmt.Errorf("channel %s: a widget needs origins", ch.ID)
			}
			for _, o := range ch.Origins {
				if _, ok := parseOrigin(o, true); !ok {
					return fmt.Errorf("channel %s: origin %q must look like https://example.com or https://*.example.com", ch.ID, o)
				}
			}
			if ch.Turnstile == nil || ch.Turnstile.SiteKey == "" || ch.Turnstile.Secret == "" {
				return fmt.Errorf("channel %s: a widget needs turnstile.site_key and turnstile.secret", ch.ID)
			}
		case KindDiscord:
			if err := validateDiscord(ch); err != nil {
				return err
			}
		default:
			return fmt.Errorf("channel %s: unknown kind %q", ch.ID, ch.Kind)
		}
	}
	return nil
}

// Spec finds a channel by id.
func (c Config) Spec(id string) (ChannelSpec, bool) {
	for _, ch := range c.Channels {
		if ch.ID == id {
			return ch, true
		}
	}
	return ChannelSpec{}, false
}

// Open builds the channel a spec describes.
func (c Config) Open(spec ChannelSpec) (Channel, error) {
	switch spec.Kind {
	case KindWidget:
		return &Widget{Spec: spec, SMTP: c.SMTP}, nil
	case KindDiscord:
		return openDiscord(spec)
	}
	return nil, fmt.Errorf("unknown channel kind %q", spec.Kind)
}
