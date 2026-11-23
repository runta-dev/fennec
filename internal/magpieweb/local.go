package magpieweb

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

type Local struct {
	mu           sync.Mutex
	server       *http.Server
	address, key string
	transport    *http.Transport
	callback     *callbackBridge
}

func (s *Local) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		s.callback.close()
		s.server.Close()
		s.transport.CloseIdleConnections()
		s.server = nil
	}
}

// URL creates one loopback origin per account/runtime lifetime. Every request
// checks the current mode and identity, including requests on reused sockets.
func (s *Local) URL(ctx context.Context, dial func(context.Context) (net.Conn, error), valid func() bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server == nil {
		key, err := token()
		if err != nil {
			return "", err
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		s.address = ln.Addr().String()
		s.key = key
		address := s.address
		origin := "http://" + address
		target, _ := url.Parse("http://magpie")
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) }, IdleConnTimeout: 30 * time.Second}
		s.transport = transport
		bridge := &callbackBridge{origin: origin, transport: transport, valid: func() bool { return ctx.Err() == nil && valid() }}
		s.callback = bridge
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Transport = transport
		proxy.ModifyResponse = func(r *http.Response) error {
			if err := bridge.observe(r); err != nil {
				return err
			}
			return preparePanelResponse(r)
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "Magpie control panel is reconnecting. Reload to try again.", 502)
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Referrer-Policy", "no-referrer")
			if r.Host != address || ctx.Err() != nil || !valid() || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin) {
				http.Error(w, "Forbidden", 403)
				return
			}
			if r.URL.Path == "/" && r.URL.Query().Get("fennec_access") == key {
				w.Header().Set("Cache-Control", "no-store")
				http.SetCookie(w, &http.Cookie{Name: "fennec_magpie", Value: key, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
				http.Redirect(w, r, "/?view=agents", http.StatusSeeOther)
				return
			}
			// Cross-site OAuth navigation omits the Strict panel cookie. The
			// short-lived state binds this route to an observed sign-in only.
			if r.URL.Path == "/auth/callback" {
				bridge.complete(w, r)
				return
			}
			cookie, err := r.Cookie("fennec_magpie")
			if err != nil || cookie.Value != key {
				http.Error(w, "Open the control panel from Fennec Settings", 403)
				return
			}
			proxy.ServeHTTP(w, r)
		})
		s.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
		go s.server.Serve(ln)
	}
	return "http://" + s.address + "/?fennec_access=" + s.key, nil
}
