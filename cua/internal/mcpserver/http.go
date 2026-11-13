package mcpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Streamable HTTP transport limits.
const (
	HTTPPath          = "/mcp"
	MaxHTTPBody       = 8 << 20
	HTTPHeaderTimeout = 10 * time.Second
	MaxHTTPConns      = 64
	// TokenEnv names the variable that carries the bearer token.
	TokenEnv = "FENNEC_CUA_HTTP_TOKEN"
	// SessionHeader carries the session id initialize issues.
	SessionHeader = "Mcp-Session-Id"
)

// HTTPConfig configures the HTTP transport. Zero limits take the defaults.
type HTTPConfig struct {
	// Token is the bearer token every request must carry; required, even
	// on a loopback address.
	Token         string
	MaxBody       int64
	HeaderTimeout time.Duration
	MaxConns      int
}

// HTTP serves MCP over Streamable HTTP: one JSON-RPC message per POST
// answered with one JSON reply. Each session (issued by initialize in the
// Mcp-Session-Id header) is its own agent with its own cancellation.
type HTTP struct {
	h    Handler
	info Info
	cfg  HTTPConfig

	mu       sync.Mutex
	sessions map[string]*server
	wg       sync.WaitGroup
}

// ErrNoToken refuses an HTTP transport without a bearer token.
var ErrNoToken = errors.New("MCP over HTTP needs a bearer token, even on a loopback address: set " + TokenEnv)

// NewHTTP builds the transport; it fails without a token.
func NewHTTP(h Handler, info Info, cfg HTTPConfig) (*HTTP, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, ErrNoToken
	}
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = MaxHTTPBody
	}
	if cfg.HeaderTimeout <= 0 {
		cfg.HeaderTimeout = HTTPHeaderTimeout
	}
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = MaxHTTPConns
	}
	if info.IDPrefix == "" {
		info.IDPrefix = "mcp-http"
	}
	return &HTTP{h: h, info: info, cfg: cfg, sessions: map[string]*server{}}, nil
}

// Serve answers connections on ln until ctx ends; calls still running are
// then cancelled.
func (s *HTTP) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: s.cfg.HeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(limitListener(ln, s.cfg.MaxConns)) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	s.wg.Wait()
	if sc, ok := s.h.(SessionCloser); ok {
		s.mu.Lock()
		for id, sv := range s.sessions {
			sc.CloseSession(&sv.sess)
			delete(s.sessions, id)
		}
		s.mu.Unlock()
	}
	return nil
}

// ListenAndServe binds addr and serves until ctx ends.
func (s *HTTP) ListenAndServe(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// ServeHTTP checks, in order: token (401), path (404), method (405),
// Origin (403), size (413), JSON shape (400), session (400 or 404). Every
// refusal closes the connection.
func (s *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	refuse := func(code int, msg string) {
		w.Header().Set("Connection", "close")
		http.Error(w, msg, code)
	}
	if !s.authorized(r.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		refuse(http.StatusUnauthorized, "a valid bearer token is required")
		return
	}
	if r.URL.Path != HTTPPath {
		refuse(http.StatusNotFound, "not found; MCP is served at "+HTTPPath)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		refuse(http.StatusMethodNotAllowed, "only POST is served")
		return
	}
	if !LocalOrigin(r.Header.Get("Origin")) {
		refuse(http.StatusForbidden, "origin not allowed")
		return
	}
	if r.ContentLength > s.cfg.MaxBody {
		refuse(http.StatusRequestEntityTooLarge, fmt.Sprintf("body over %d bytes", s.cfg.MaxBody))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			refuse(http.StatusRequestEntityTooLarge, fmt.Sprintf("body over %d bytes", s.cfg.MaxBody))
			return
		}
		refuse(http.StatusBadRequest, "unreadable body")
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		refuse(http.StatusBadRequest, "batch requests are not supported; send one JSON-RPC message per request")
		return
	}
	var m message
	if !json.Valid(body) || json.Unmarshal(body, &m) != nil {
		refuse(http.StatusBadRequest, "the body is not one JSON-RPC message")
		return
	}
	var sess *server
	if m.Method == "initialize" {
		sess = s.newSession()
		w.Header().Set(SessionHeader, sess.info.FixedID)
	} else {
		id := r.Header.Get(SessionHeader)
		if id == "" {
			refuse(http.StatusBadRequest, "missing "+SessionHeader+"; send initialize first")
			return
		}
		s.mu.Lock()
		sess = s.sessions[id]
		s.mu.Unlock()
		if sess == nil {
			refuse(http.StatusNotFound, "unknown session; initialize again")
			return
		}
	}
	p := sess.dispatch(r.Context(), body, &s.wg)
	if p != nil {
		<-p.done
	}
	if p == nil || p.reply == nil {
		// A notification, or a request cancelled before it answered.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	p.reply.JSONRPC = "2.0"
	data, err := json.Marshal(p.reply)
	if err != nil {
		data, _ = json.Marshal(reply{JSONRPC: "2.0", ID: p.reply.ID, Error: &rpcError{Code: CodeInternal, Message: err.Error()}})
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (s *HTTP) newSession() *server {
	var b [16]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	info := s.info
	info.FixedID = id
	sv := &server{h: s.h, info: info, running: map[string]context.CancelFunc{}}
	sv.sess.AgentID = agentID(info.IDPrefix, "", id)
	s.mu.Lock()
	s.sessions[id] = sv
	s.mu.Unlock()
	return sv
}

func (s *HTTP) authorized(header string) bool {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(s.cfg.Token)) == 1
}

// LocalOrigin accepts a missing Origin (a non-browser client) and browser
// origins on localhost, 127.0.0.1 or [::1] only.
func LocalOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// limitListener accepts at most n connections at a time.
func limitListener(ln net.Listener, n int) net.Listener {
	return &limited{Listener: ln, sem: make(chan struct{}, n)}
}

type limited struct {
	net.Listener
	sem chan struct{}
}

func (l *limited) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.sem }}, nil
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
