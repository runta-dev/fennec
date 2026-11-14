// Package browserterminal exposes session-scoped attachments on loopback only.
package browserterminal

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"fennec/internal/terminalstream"
	uiassets "fennec/native/macos/UI"
	"github.com/gorilla/websocket"
)

type Attach func(context.Context) (net.Conn, string, error)
type Metadata struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	Icon  string `json:"icon"`
}
type grant struct {
	ctx      context.Context
	attach   Attach
	expires  time.Time
	id       string
	active   bool
	metadata func() Metadata
}
type Server struct {
	mu     sync.Mutex
	server *http.Server
	origin string
	grants map[string]*grant
}

func (s *Server) URL(ctx context.Context, id string, attach Attach, metadata ...func() Metadata) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server == nil {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		s.origin = "http://" + listener.Addr().String()
		s.grants = map[string]*grant{}
		s.server = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
		go s.server.Serve(listener)
	}
	for token, g := range s.grants {
		if g.ctx.Err() != nil || time.Now().After(g.expires) {
			delete(s.grants, token)
		}
	}
	if len(s.grants) >= 256 {
		return "", fmt.Errorf("too many browser attachments")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	s.grants[token] = &grant{ctx: ctx, attach: attach, id: id, expires: time.Now().Add(24 * time.Hour)}
	if len(metadata) > 0 {
		s.grants[token].metadata = metadata[0]
	}
	return s.origin + "/#" + token, nil
}
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if "http://"+r.Host != s.origin {
		http.Error(w, "Forbidden", 403)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'")
	if r.URL.Path == "/socket" {
		s.socket(w, r)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "browser.html"
	}
	data, err := fs.ReadFile(uiassets.BrowserAssets, path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(path, ".js"):
		w.Header().Set("Content-Type", "application/javascript")
	case strings.HasSuffix(path, ".css"):
		w.Header().Set("Content-Type", "text/css")
	case strings.HasSuffix(path, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.Write(data)
}
func (s *Server) socket(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == s.origin }}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(100000)
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hello struct {
		Token   string `json:"token"`
		Columns int    `json:"columns"`
		Rows    int    `json:"rows"`
	}
	if ws.ReadJSON(&hello) != nil || hello.Columns < 1 || hello.Columns > 500 || hello.Rows < 1 || hello.Rows > 300 {
		return
	}
	s.mu.Lock()
	g := s.grants[hello.Token]
	if g == nil || g.ctx.Err() != nil || time.Now().After(g.expires) || g.active {
		s.mu.Unlock()
		return
	}
	g.active = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); g.active = false; s.mu.Unlock() }()
	ctx, cancel := context.WithCancel(g.ctx)
	defer cancel()
	var writeMu sync.Mutex
	write := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return ws.WriteJSON(value)
	}
	if g.metadata != nil {
		update := func() error { value := g.metadata(); value.Type = "metadata"; return write(value) }
		if update() != nil {
			return
		}
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if update() != nil {
						cancel()
						return
					}
				}
			}
		}()
	}
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	defer stop()
	conn, streamID, err := g.attach(ctx)
	if err != nil {
		write(terminalstream.Frame{Type: "error", Message: "Unable to attach. Reconnect to try again."})
		return
	}
	defer conn.Close()
	closeConn := context.AfterFunc(ctx, func() { conn.Close() })
	defer closeConn()
	encoder := json.NewEncoder(conn)
	if encoder.Encode(terminalstream.Frame{Type: "connect", ID: streamID, Columns: hello.Columns, Rows: hello.Rows, Term: "xterm-256color"}) != nil {
		return
	}
	ws.SetReadDeadline(time.Time{})
	go func() {
		defer cancel()
		for {
			var frame terminalstream.Frame
			if ws.ReadJSON(&frame) != nil {
				return
			}
			frame.ID = streamID
			switch frame.Type {
			case "input", "image":
				if len(frame.Data) > 16384 {
					return
				}
				if frame.Type == "image" && (len(frame.Data) == 0 || frame.Bytes < 0 || frame.Bytes > 20<<20) {
					return
				}
			case "credit":
				if frame.Bytes < 1 || frame.Bytes > terminalstream.Window {
					return
				}
			case "resize":
				if frame.Columns < 1 || frame.Columns > 500 || frame.Rows < 1 || frame.Rows > 300 {
					return
				}
			default:
				return
			}
			// Never forward client-controlled launch/operation/identity fields.
			safe := terminalstream.Frame{Type: frame.Type, ID: streamID, Data: frame.Data, Bytes: frame.Bytes, Rows: frame.Rows, Columns: frame.Columns}
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if encoder.Encode(safe) != nil {
				return
			}
		}
	}()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 262144)
	for scanner.Scan() {
		var frame terminalstream.Frame
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			return
		}
		if write(frame) != nil {
			return
		}
		if frame.Type == "exit" || frame.Type == "error" {
			return
		}
	}
}
