package discord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxMessageLength = 2000 // Discord's content limit
	maxRetries       = 3
	maxRetryWait     = 60 * time.Second
)

// ErrPermanent wraps Reply errors that retrying won't fix (missing access,
// deleted channel, bad reference). An outbox should give up on these.
var ErrPermanent = errors.New("discord: permanent failure")

// replyRef is what this adapter needs to answer an author: the channel the
// feedback came from, the author, and (for channel messages) the message.
// Encoded as "v1:<channel>:<user>[:<message>]".
type replyRef struct {
	Channel string
	User    string
	Message string
}

func (r replyRef) String() string {
	s := "v1:" + r.Channel + ":" + r.User
	if r.Message != "" {
		s += ":" + r.Message
	}
	return s
}

func parseReplyRef(s string) (replyRef, error) {
	parts := strings.Split(s, ":")
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "v1" {
		return replyRef{}, fmt.Errorf("%w: malformed reply ref %q", ErrPermanent, s)
	}
	r := replyRef{Channel: parts[1], User: parts[2]}
	if len(parts) == 4 {
		r.Message = parts[3]
	}
	if !isSnowflake(r.Channel) || !isSnowflake(r.User) || (r.Message != "" && !isSnowflake(r.Message)) {
		return replyRef{}, fmt.Errorf("%w: malformed reply ref %q", ErrPermanent, s)
	}
	return r, nil
}

// Reply answers the author in the channel the feedback came from: it
// mentions them and, when the feedback was a channel message, replies to
// that message. Only the author can be pinged (no @everyone, roles or other
// users, whatever the text says). Long text is split into several messages.
//
// Retrying the same (replyRef, text) shortly after a lost response doesn't
// post twice: each part carries a nonce derived from them, enforced by
// Discord.
func (a *Adapter) Reply(ctx context.Context, ref, text string) error {
	r, err := parseReplyRef(ref)
	if err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("%w: empty reply", ErrPermanent)
	}
	mention := "<@" + r.User + "> "
	parts := splitMessage(text, maxMessageLength-len(mention))
	for i, part := range parts {
		msg := map[string]any{
			"content": part,
			"allowed_mentions": map[string]any{
				"parse":        []string{},
				"users":        []string{r.User},
				"replied_user": true,
			},
			"nonce":         nonce(ref, i, text),
			"enforce_nonce": true,
		}
		if i == 0 {
			msg["content"] = mention + part
			if r.Message != "" {
				msg["message_reference"] = map[string]any{
					"message_id":         r.Message,
					"channel_id":         r.Channel,
					"fail_if_not_exists": false, // still answer if it was deleted
				}
			}
		}
		if err := a.call(ctx, http.MethodPost, "/channels/"+r.Channel+"/messages", msg, nil); err != nil {
			return err
		}
	}
	return nil
}

// RegisterCommands creates or updates the /feedback slash command, in each
// configured guild (instant) or globally when no guild is configured.
func (a *Adapter) RegisterCommands(ctx context.Context) error {
	cmd := []map[string]any{{
		"name":        a.cfg.CommandName,
		"type":        commandTypeChatInput,
		"description": "Send feedback or report a bug to the team",
		"options": []map[string]any{{
			"name":        optionText,
			"type":        optionTypeString,
			"description": "What happened, or what you'd like",
			"required":    true,
			"max_length":  commandMaxTextLength,
		}},
	}}
	base := "/applications/" + a.cfg.ApplicationID
	if len(a.guilds) == 0 {
		return a.call(ctx, http.MethodPut, base+"/commands", cmd, nil)
	}
	for g := range a.guilds {
		if err := a.call(ctx, http.MethodPut, base+"/guilds/"+g+"/commands", cmd, nil); err != nil {
			return err
		}
	}
	return nil
}

// call sends one REST request, honoring rate limits and retrying 5xx.
func (a *Adapter) call(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, a.cfg.APIBase+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", a.cfg.BotToken)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("User-Agent", "DiscordBot (https://fennec.dev, 1)")
		resp, err := a.http.Do(req)
		if err != nil {
			lastErr = err
			if !sleep(ctx, backoff(attempt)) {
				return ctx.Err()
			}
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode < 300:
			if out != nil && len(data) > 0 {
				return json.Unmarshal(data, out)
			}
			return nil
		case resp.StatusCode == http.StatusTooManyRequests:
			lastErr = fmt.Errorf("discord: %s %s: rate limited", method, path)
			if !sleep(ctx, retryAfter(resp, data)) {
				return ctx.Err()
			}
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("discord: %s %s: %s", method, path, resp.Status)
			if !sleep(ctx, backoff(attempt)) {
				return ctx.Err()
			}
		default:
			return fmt.Errorf("%w: %s %s: %s: %s", ErrPermanent, method, path, resp.Status, strings.TrimSpace(string(data)))
		}
	}
	return lastErr
}

func retryAfter(resp *http.Response, body []byte) time.Duration {
	var rl struct {
		RetryAfter float64 `json:"retry_after"`
	}
	d := time.Second
	if json.Unmarshal(body, &rl) == nil && rl.RetryAfter > 0 {
		d = time.Duration(rl.RetryAfter * float64(time.Second))
	} else if s, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil && s > 0 {
		d = time.Duration(s * float64(time.Second))
	}
	return min(d, maxRetryWait)
}

func backoff(attempt int) time.Duration { return time.Duration(1<<attempt) * 500 * time.Millisecond }

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// nonce is a stable 25-character ID for part i of a reply, so Discord drops
// a duplicate when the same reply is retried.
func nonce(ref string, i int, text string) string {
	h := sha256.Sum256([]byte(ref + "\x00" + strconv.Itoa(i) + "\x00" + text))
	return hex.EncodeToString(h[:])[:25]
}

// splitMessage cuts text into parts of at most limit bytes, preferring
// paragraph, then line, then word boundaries, never splitting a rune.
func splitMessage(text string, limit int) []string {
	var parts []string
	for len(text) > limit {
		cut := limit
		for !utf8.RuneStart(text[cut]) {
			cut--
		}
		window := text[:cut]
		for _, sep := range []string{"\n\n", "\n", " "} {
			if i := strings.LastIndex(window, sep); i > limit/2 {
				cut = i
				break
			}
		}
		parts = append(parts, strings.TrimRight(text[:cut], " \n"))
		text = strings.TrimLeft(text[cut:], " \n")
	}
	if text = strings.TrimRight(text, " \n"); text != "" {
		parts = append(parts, text)
	}
	return parts
}
