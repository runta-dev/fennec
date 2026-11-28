package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Gateway opcodes and intents (API v10).
const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatAck   = 11

	intentGuildMessages  = 1 << 9
	intentMessageContent = 1 << 15

	messageTypeDefault = 0
	messageTypeReply   = 19

	maxReconnectWait = 2 * time.Minute
	receiptEmoji     = "%E2%9C%85" // ✅, URL-escaped
)

// ErrFatalGateway is returned by Run when Discord refuses the bot for a
// reason reconnecting won't fix (bad token, intents not enabled).
var ErrFatalGateway = errors.New("discord: gateway refused the bot")

// fatalCloseCodes end Run: 4004 authentication failed, 4010 invalid shard,
// 4011 sharding required, 4012 invalid API version, 4013 invalid intents,
// 4014 disallowed intents (Message Content not enabled in the portal).
var fatalCloseCodes = map[int]string{
	4004: "authentication failed (check the bot token)",
	4010: "invalid shard",
	4011: "sharding required",
	4012: "invalid API version",
	4013: "invalid intents",
	4014: "disallowed intents (enable Message Content Intent in the Developer Portal)",
}

// Run reads the Discord Gateway and files every new message posted in a
// watched channel as feedback. It reconnects (resuming when it can) until
// ctx ends or Discord refuses the bot outright.
func (a *Adapter) Run(ctx context.Context, sink Sink) error {
	if len(a.channels) == 0 {
		<-ctx.Done() // nothing to watch; /feedback still works over HTTP
		return ctx.Err()
	}
	var s gatewaySession
	failures := 0
	for {
		ready, err := a.runOnce(ctx, sink, &s)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrFatalGateway) {
			return err
		}
		if ready {
			failures = 0
		}
		wait := min(time.Duration(1<<min(failures, 7))*time.Second, maxReconnectWait)
		wait += time.Duration(rand.Int64N(int64(time.Second)))
		log.Printf("discord: gateway disconnected (%v), reconnecting in %s", err, wait.Round(time.Second))
		failures++
		if !sleep(ctx, wait) {
			return ctx.Err()
		}
	}
}

// gatewaySession is what a resume needs; it survives reconnects.
type gatewaySession struct {
	id        string
	resumeURL string
	seq       atomic.Int64
}

func (s *gatewaySession) reset() {
	s.id, s.resumeURL = "", ""
	s.seq.Store(0)
}

type gatewayPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

// runOnce holds one websocket connection. ready reports whether the
// connection got as far as READY or RESUMED.
func (a *Adapter) runOnce(ctx context.Context, sink Sink, s *gatewaySession) (ready bool, err error) {
	base := s.resumeURL
	if base == "" {
		if base, err = a.gatewayURL(ctx); err != nil {
			return false, err
		}
	}
	u, err := url.Parse(base)
	if err != nil {
		return false, err
	}
	q := u.Query()
	q.Set("v", "10")
	q.Set("encoding", "json")
	u.RawQuery = q.Encode()

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	conn.SetReadLimit(4 << 20)

	var writeMu sync.Mutex
	send := func(op int, d any) error {
		raw, err := json.Marshal(d)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(gatewayPayload{Op: op, D: raw})
	}

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-connCtx.Done(); conn.Close() }()

	var hello gatewayPayload
	if err := conn.ReadJSON(&hello); err != nil {
		return false, err
	}
	if hello.Op != opHello {
		return false, fmt.Errorf("discord: expected hello, got op %d", hello.Op)
	}
	var h struct {
		HeartbeatInterval int64 `json:"heartbeat_interval"`
	}
	if err := json.Unmarshal(hello.D, &h); err != nil || h.HeartbeatInterval <= 0 {
		return false, fmt.Errorf("discord: bad hello")
	}

	token := strings.TrimPrefix(a.cfg.BotToken, "Bot ")
	if s.id != "" {
		err = send(opResume, map[string]any{"token": token, "session_id": s.id, "seq": s.seq.Load()})
	} else {
		err = send(opIdentify, map[string]any{
			"token":   token,
			"intents": intentGuildMessages | intentMessageContent,
			"properties": map[string]string{
				"os": "linux", "browser": "fennec", "device": "fennec",
			},
		})
	}
	if err != nil {
		return false, err
	}

	// Heartbeat: first beat after interval*jitter, then every interval. A
	// beat that finds the previous one unacknowledged means a zombie
	// connection, so drop it and resume.
	var acked atomic.Bool
	acked.Store(true)
	beat := func() error {
		var seq any
		if n := s.seq.Load(); n > 0 {
			seq = n
		}
		return send(opHeartbeat, seq)
	}
	go func() {
		interval := time.Duration(h.HeartbeatInterval) * time.Millisecond
		if !sleep(connCtx, time.Duration(rand.Float64()*float64(interval))) {
			return
		}
		for {
			if !acked.Swap(false) {
				log.Printf("discord: heartbeat not acknowledged, reconnecting")
				cancel()
				return
			}
			if beat() != nil {
				cancel()
				return
			}
			if !sleep(connCtx, interval) {
				return
			}
		}
	}()

	for {
		var p gatewayPayload
		if err := conn.ReadJSON(&p); err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) {
				if why, ok := fatalCloseCodes[ce.Code]; ok {
					return ready, fmt.Errorf("%w: %s", ErrFatalGateway, why)
				}
				if ce.Code == 4007 || ce.Code == 4009 { // bad seq, session timed out
					s.reset()
				}
			}
			return ready, err
		}
		if p.S != nil {
			s.seq.Store(*p.S)
		}
		switch p.Op {
		case opHeartbeat:
			if err := beat(); err != nil {
				return ready, err
			}
		case opHeartbeatAck:
			acked.Store(true)
		case opReconnect:
			return ready, errors.New("discord asked to reconnect")
		case opInvalidSession:
			var resumable bool
			_ = json.Unmarshal(p.D, &resumable)
			if !resumable {
				s.reset()
			}
			return ready, errors.New("invalid session")
		case opDispatch:
			switch p.T {
			case "READY":
				var r struct {
					SessionID        string `json:"session_id"`
					ResumeGatewayURL string `json:"resume_gateway_url"`
				}
				_ = json.Unmarshal(p.D, &r)
				s.id, s.resumeURL = r.SessionID, r.ResumeGatewayURL
				ready = true
				log.Printf("discord: gateway ready, watching %d channel(s)", len(a.channels))
			case "RESUMED":
				ready = true
			case "MESSAGE_CREATE":
				var m gatewayMessage
				if json.Unmarshal(p.D, &m) == nil {
					if f, ok := a.messageFeedback(m); ok {
						go a.fileMessage(ctx, sink, m, f)
					}
				}
			}
		}
	}
}

func (a *Adapter) gatewayURL(ctx context.Context) (string, error) {
	if a.cfg.GatewayURL != "" {
		return a.cfg.GatewayURL, nil
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := a.call(ctx, http.MethodGet, "/gateway/bot", nil, &out); err != nil {
		if errors.Is(err, ErrPermanent) {
			return "", fmt.Errorf("%w: %v", ErrFatalGateway, err)
		}
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("discord: /gateway/bot returned no url")
	}
	return out.URL, nil
}

// gatewayMessage is the subset of a MESSAGE_CREATE we read.
type gatewayMessage struct {
	ID        string      `json:"id"`
	ChannelID string      `json:"channel_id"`
	GuildID   string      `json:"guild_id"`
	Type      int         `json:"type"`
	Content   string      `json:"content"`
	WebhookID string      `json:"webhook_id"`
	Author    discordUser `json:"author"`
	Member    *struct {
		Nick string `json:"nick"`
	} `json:"member"`
	Attachments []struct {
		Filename string `json:"filename"`
		URL      string `json:"url"`
	} `json:"attachments"`
}

// messageFeedback decides whether a message is feedback and normalizes it.
// Bots, webhooks, system messages and unwatched channels are skipped.
func (a *Adapter) messageFeedback(m gatewayMessage) (Feedback, bool) {
	if !a.channels[m.ChannelID] || m.Author.Bot || m.WebhookID != "" ||
		(m.Type != messageTypeDefault && m.Type != messageTypeReply) ||
		!isSnowflake(m.ID) || !isSnowflake(m.Author.ID) {
		return Feedback{}, false
	}
	text := strings.TrimSpace(m.Content)
	var files []string
	for _, at := range m.Attachments {
		files = append(files, at.URL)
	}
	if text == "" && len(files) == 0 {
		return Feedback{}, false
	}
	au := m.Author.author()
	if m.Member != nil && m.Member.Nick != "" {
		au.Name = m.Member.Nick
	}
	ctx := map[string]string{
		"source":     "message",
		"guild_id":   m.GuildID,
		"channel_id": m.ChannelID,
		"message_id": m.ID,
		"link":       "https://discord.com/channels/" + orAtMe(m.GuildID) + "/" + m.ChannelID + "/" + m.ID,
	}
	if len(files) > 0 {
		ctx["attachments"] = strings.Join(files, "\n")
	}
	return Feedback{
		Channel:  ChannelName,
		Author:   au,
		Text:     text,
		Context:  ctx,
		ReplyRef: replyRef{Channel: m.ChannelID, User: m.Author.ID, Message: m.ID}.String(),
	}, true
}

// fileMessage submits one channel message and marks it ✅ once filed, so the
// author sees it was picked up. The reaction is best effort.
func (a *Adapter) fileMessage(ctx context.Context, sink Sink, m gatewayMessage, f Feedback) {
	sctx, cancel := context.WithTimeout(ctx, backgroundSubmitTimeout)
	defer cancel()
	if _, err := sink.Submit(sctx, f); err != nil {
		log.Printf("discord: filing message %s failed: %v", m.ID, err)
		return
	}
	path := "/channels/" + m.ChannelID + "/messages/" + m.ID + "/reactions/" + receiptEmoji + "/@me"
	if err := a.call(sctx, http.MethodPut, path, nil, nil); err != nil {
		log.Printf("discord: receipt reaction on %s: %v", m.ID, err)
	}
}

func orAtMe(guild string) string {
	if guild == "" {
		return "@me"
	}
	return guild
}
