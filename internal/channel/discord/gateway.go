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

	intentGuilds         = 1 << 0 // THREAD_CREATE, so forum posts are known
	intentGuildMessages  = 1 << 9
	intentMessageContent = 1 << 15 // privileged: enable it in the Developer Portal

	maxReconnectWait = 2 * time.Minute
	queueSize        = 1024
)

// ErrFatalGateway is returned by Run when Discord refuses the bot for a
// reason reconnecting won't fix (bad token, intents not enabled).
var ErrFatalGateway = errors.New("discord: gateway refused the bot")

// fatalCloseCodes end Run.
var fatalCloseCodes = map[int]string{
	4004: "authentication failed (check the bot token)",
	4010: "invalid shard",
	4011: "sharding required",
	4012: "invalid API version",
	4013: "invalid intents",
	4014: "disallowed intents (enable Message Content Intent in the Developer Portal)",
}

// Run reads the Discord Gateway and handles mentions, forum posts and
// thread follow-ups (see handle). It reconnects, resuming when it can, until
// ctx ends or Discord refuses the bot outright.
func (a *Adapter) Run(ctx context.Context, sink Sink) error {
	// One worker handles messages in order, so a thread's first message is
	// filed before its follow-ups, and a slow board never stalls the socket.
	queue := make(chan gatewayMessage, queueSize)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-queue:
				a.handle(ctx, sink, m)
			}
		}
	}()
	defer wg.Wait()

	var s gatewaySession
	failures := 0
	for {
		ready, err := a.runOnce(ctx, queue, &s)
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
func (a *Adapter) runOnce(ctx context.Context, queue chan<- gatewayMessage, s *gatewaySession) (ready bool, err error) {
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
			"intents": intentGuilds | intentGuildMessages | intentMessageContent,
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
					SessionID        string      `json:"session_id"`
					ResumeGatewayURL string      `json:"resume_gateway_url"`
					User             discordUser `json:"user"`
				}
				_ = json.Unmarshal(p.D, &r)
				s.id, s.resumeURL = r.SessionID, r.ResumeGatewayURL
				a.mu.Lock()
				a.botID = r.User.ID
				a.mu.Unlock()
				ready = true
				log.Printf("discord: gateway ready as %s, %d forum(s)", r.User.Username, len(a.forums))
			case "RESUMED":
				ready = true
			case "THREAD_CREATE", "CHANNEL_CREATE":
				var c channelInfo
				if json.Unmarshal(p.D, &c) == nil && c.ID != "" {
					a.remember(c)
				}
			case "MESSAGE_CREATE":
				var m gatewayMessage
				if json.Unmarshal(p.D, &m) == nil {
					select {
					case queue <- m:
					default:
						log.Printf("discord: queue full, dropped message %s", m.ID)
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
