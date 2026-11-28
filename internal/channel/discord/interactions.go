package discord

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Interaction and response types from the Discord API.
const (
	interactionPing         = 1
	interactionCommand      = 2
	responsePong            = 1
	responseMessage         = 4
	messageFlagEphemeral    = 1 << 6
	maxInteractionBody      = 64 << 10
	maxSignatureAge         = 5 * time.Minute
	optionText              = "text"
	optionTypeString        = 3
	commandTypeChatInput    = 1
	commandMaxTextLength    = 2000
	backgroundSubmitTimeout = 30 * time.Second
	ackFiled                = "Thanks, got it (#%d). We'll reply here when it's fixed."
	ackQueued               = "Thanks, got it. We'll reply here when it's fixed."
	ackEmpty                = "Please describe what happened, e.g. `/feedback text: the app crashes when …`"
	ackFailed               = "Sorry, we couldn't record that just now. Please try again in a minute."
	ackWrongServer          = "Feedback isn't collected from this server."
)

// submitBudget is how long /feedback waits for the request to be filed
// before acknowledging anyway: Discord gives an interaction 3 s to answer.
var submitBudget = 2 * time.Second

// InteractionsHandler serves the Interactions Endpoint URL set in the
// Discord Developer Portal. Mount it on the public intake service, e.g.
// mux.Handle("POST /discord/interactions", a.InteractionsHandler(sink)).
func (a *Adapter) InteractionsHandler(sink Sink) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxInteractionBody+1))
		if err != nil || len(body) > maxInteractionBody {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !a.verify(r.Header.Get("X-Signature-Ed25519"), r.Header.Get("X-Signature-Timestamp"), body) {
			// Discord probes the endpoint with bad signatures and expects 401.
			http.Error(w, "invalid request signature", http.StatusUnauthorized)
			return
		}
		var in interaction
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch in.Type {
		case interactionPing:
			writeJSON(w, map[string]any{"type": responsePong})
		case interactionCommand:
			writeJSON(w, a.handleCommand(r.Context(), sink, in))
		default:
			http.Error(w, "unsupported interaction", http.StatusBadRequest)
		}
	})
}

// verify checks Discord's ed25519 signature over timestamp+body and that the
// timestamp is recent, so a captured request can't be replayed later.
func (a *Adapter) verify(sigHex, timestamp string, body []byte) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize || timestamp == "" {
		return false
	}
	msg := make([]byte, 0, len(timestamp)+len(body))
	msg = append(msg, timestamp...)
	msg = append(msg, body...)
	if !ed25519.Verify(a.publicKey, msg, sig) {
		return false
	}
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	age := a.now().Sub(time.Unix(sec, 0))
	return age < maxSignatureAge && age > -maxSignatureAge
}

func (a *Adapter) handleCommand(ctx context.Context, sink Sink, in interaction) map[string]any {
	if in.Data.Name != a.cfg.CommandName {
		return ephemeral("Unknown command.")
	}
	if len(a.guilds) > 0 && !a.guilds[in.GuildID] {
		return ephemeral(ackWrongServer)
	}
	user := in.author()
	text := strings.TrimSpace(in.Data.option(optionText))
	if text == "" || user.ID == "" || !isSnowflake(in.ChannelID) {
		return ephemeral(ackEmpty)
	}
	f := Feedback{
		Channel: ChannelName,
		Author:  user,
		Text:    text,
		Context: map[string]string{
			"source":     "command",
			"guild_id":   in.GuildID,
			"channel_id": in.ChannelID,
		},
		ReplyRef: replyRef{Channel: in.ChannelID, User: user.ID}.String(),
	}

	// Answer within Discord's 3 s window even when filing is slow: wait up
	// to submitBudget, then acknowledge and let the submit finish alone.
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundSubmitTimeout)
		defer cancel()
		n, err := sink.Submit(sctx, f)
		if err != nil {
			log.Printf("discord: filing /%s from %s failed: %v", a.cfg.CommandName, user.ID, err)
		}
		done <- result{n, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			return ephemeral(ackFailed)
		}
		if res.n > 0 {
			return ephemeral(fmt.Sprintf(ackFiled, res.n))
		}
		return ephemeral(ackQueued)
	case <-time.After(submitBudget):
		return ephemeral(ackQueued)
	}
}

func ephemeral(text string) map[string]any {
	return map[string]any{
		"type": responseMessage,
		"data": map[string]any{
			"content":          text,
			"flags":            messageFlagEphemeral,
			"allowed_mentions": map[string]any{"parse": []string{}},
		},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// interaction is the subset of a Discord Interaction we read.
type interaction struct {
	Type      int    `json:"type"`
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	Member    *struct {
		Nick string      `json:"nick"`
		User discordUser `json:"user"`
	} `json:"member"`
	User *discordUser `json:"user"` // set in DMs instead of member
	Data commandData  `json:"data"`
}

type commandData struct {
	Name    string `json:"name"`
	Options []struct {
		Name  string          `json:"name"`
		Type  int             `json:"type"`
		Value json.RawMessage `json:"value"`
	} `json:"options"`
}

// option returns a string option's value, or "" when absent.
func (d commandData) option(name string) string {
	for _, o := range d.Options {
		if o.Name != name || o.Type != optionTypeString {
			continue
		}
		var v string
		if json.Unmarshal(o.Value, &v) == nil {
			return v
		}
	}
	return ""
}

func (in interaction) author() Author {
	switch {
	case in.Member != nil:
		au := in.Member.User.author()
		if in.Member.Nick != "" {
			au.Name = in.Member.Nick
		}
		return au
	case in.User != nil:
		return in.User.author()
	}
	return Author{}
}

type discordUser struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Bot        bool   `json:"bot"`
}

func (u discordUser) author() Author {
	name := u.GlobalName
	if name == "" {
		name = u.Username
	}
	return Author{ID: u.ID, Name: name, Handle: u.Username}
}
