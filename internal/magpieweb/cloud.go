// Package magpieweb connects the cloud control panel to a private local browser.
package magpieweb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Cloud owns only the web process it starts. Its access key stays on the cloud.
type Cloud struct {
	mu           sync.Mutex
	address, key string
	cancel       context.CancelFunc
	done         <-chan struct{}
}

func (c *Cloud) Close() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.address = ""
	done := c.done
	c.mu.Unlock()
	// Reap the owned child before terminal serve exits; cancel alone schedules
	// an asynchronous kill that can lose the race with the parent's os.Exit.
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
}
func enabled(home string) bool {
	b, _ := os.ReadFile(filepath.Join(home, ".config/fennec/gateway-enabled"))
	return strings.TrimSpace(string(b)) == "true"
}
func token() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func (c *Cloud) start(ctx context.Context, home string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !enabled(home) {
		return fmt.Errorf("Magpie is disabled")
	}
	if c.address != "" {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	address := ln.Addr().String()
	ln.Close()
	key, err := token()
	if err != nil {
		return err
	}
	life, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(life, filepath.Join(home, ".local/bin/magpie"), "web", "--no-open", "--addr", address)
	cmd.Env = append(os.Environ(), "HOME="+home, "MAGPIE_ADDR=127.0.0.1:3425", "MAGPIE_WEB_KEY="+key, "DBUS_SESSION_BUS_ADDRESS=disabled:", "CODEX_HOME="+home+"/.local/fennec/native/codex", "CLAUDE_CONFIG_DIR="+home+"/.local/fennec/native/claude", "PI_CODING_AGENT_DIR="+home+"/.local/fennec/native/pi", "KIMI_CODE_HOME="+home+"/.local/fennec/native/kimi")
	// Magpie prints its authenticated link; never forward its output to logs.
	if err = cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("Magpie control panel could not start")
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	ready, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	for {
		conn, e := (&net.Dialer{}).DialContext(ready, "tcp", address)
		if e == nil {
			conn.Close()
			break
		}
		select {
		case <-done:
			cancel()
			return fmt.Errorf("Magpie control panel stopped")
		case <-ready.Done():
			cancel()
			return fmt.Errorf("Magpie control panel is not ready")
		case <-time.After(50 * time.Millisecond):
		}
	}
	c.address, c.key, c.cancel, c.done = address, key, cancel, done
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				c.mu.Lock()
				if c.address == address {
					c.address = ""
					c.cancel = nil
				}
				c.mu.Unlock()
				return
			case <-ticker.C:
				if !enabled(home) {
					cancel()
				}
			}
		}
	}()
	return nil
}
func (c *Cloud) Dial(ctx context.Context, home string) (net.Conn, error) {
	if err := c.start(ctx, home); err != nil {
		return nil, err
	}
	c.mu.Lock()
	address, key := c.address, c.key
	c.mu.Unlock()
	target, _ := url.Parse("http://" + address)
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.Host = address
		if r.Header.Get("Origin") != "" {
			r.Header.Set("Origin", target.String())
		}
		r.Header.Del("Cookie")
		_, port, _ := net.SplitHostPort(address)
		r.AddCookie(&http.Cookie{Name: "magpie_web_" + port, Value: key})
	}
	proxy.ModifyResponse = func(r *http.Response) error { r.Header.Del("Set-Cookie"); return nil }
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "Magpie control panel is unavailable", 502)
	}
	client, server := net.Pipe()
	listener := &singleListener{conn: server, done: make(chan struct{})}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !enabled(home) {
			http.Error(w, "Magpie is disabled", 403)
			return
		}
		proxy.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 10 * time.Second}
	srv.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed || state == http.StateHijacked {
			listener.Close()
		}
	}
	go srv.Serve(listener)
	return client, nil
}

type singleListener struct {
	mu   sync.Mutex
	conn net.Conn
	done chan struct{}
	once sync.Once
}

func (l *singleListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	c := l.conn
	l.conn = nil
	l.mu.Unlock()
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *singleListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *singleListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
