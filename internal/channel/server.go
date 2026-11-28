package channel

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"fennec/internal/board"
	"fennec/internal/localization"
	uiassets "fennec/native/macos/UI"
)

// Server is the public intake. It serves only the widget script and the
// submission routes; it never proxies to the board's API. The config is read
// on every request, so channel edits apply without a restart.
type Server struct {
	Home string
	Log  *log.Logger
}

// Handler routes the public endpoints.
func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /widget.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write(widgetJS)
	})
	mux.HandleFunc("GET /v1/widget/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, widget, ok := s.widget(w, r)
		if ok {
			writeJSON(w, http.StatusOK, map[string]any{"site_key": widget.Spec.Turnstile.SiteKey, "strings": widgetStrings(r.URL.Query().Get("lang"))})
		}
	})
	mux.HandleFunc("OPTIONS /v1/widget/{id}/feedback", func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := s.widget(w, r); ok {
			w.Header().Set("Access-Control-Allow-Methods", "POST")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /v1/widget/{id}/feedback", func(w http.ResponseWriter, r *http.Request) {
		spec, widget, ok := s.widget(w, r)
		if !ok {
			return
		}
		fb, err := widget.Ingest(r)
		if errors.Is(err, ErrRejected) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			s.logf("channel %s: ingest: %v", spec.ID, err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "unavailable"})
			return
		}
		req, err := Deliver(s.Home, spec, fb)
		if errors.Is(err, ErrOff) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.logf("channel %s: deliver: %v", spec.ID, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unavailable"})
			return
		}
		s.logf("channel %s: filed request %d in %s", spec.ID, req.Number, spec.Project)
		writeJSON(w, http.StatusCreated, map[string]bool{"ok": true, "can_reply": fb.ReplyRef != ""})
	})
	mux.HandleFunc("POST /v1/discord/{id}/interactions", s.discordInteractions)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	return mux
}

// widget resolves the route's channel and checks the page's origin, setting
// the CORS headers for an allowed one.
func (s Server) widget(w http.ResponseWriter, r *http.Request) (ChannelSpec, *Widget, bool) {
	cfg, err := LoadConfig(s.Home)
	if err != nil {
		s.logf("config: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
		return ChannelSpec{}, nil, false
	}
	spec, ok := cfg.Spec(r.PathValue("id"))
	if !ok || spec.Kind != KindWidget || !s.on(spec) {
		http.NotFound(w, r)
		return spec, nil, false
	}
	widget := &Widget{Spec: spec, SMTP: cfg.SMTP}
	origin := r.Header.Get("Origin")
	w.Header().Add("Vary", "Origin")
	if !widget.AllowedOrigin(origin) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin not allowed"})
		return spec, nil, false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	return spec, widget, true
}

// widgetStrings is the widget's copy (widget.* in the shared catalog) in
// the visitor's language, e.g. "zh-CN" for "zh" or "zh-Hans-CN".
func widgetStrings(lang string) map[string]string {
	catalog, err := localization.Decode(uiassets.Translations)
	if err != nil {
		return nil
	}
	locale := "en-US"
	switch lower := strings.ToLower(lang); {
	case strings.HasPrefix(lower, "zh"):
		locale = "zh-CN"
	case lower == "en-gb":
		locale = "en-GB"
	}
	out := map[string]string{}
	for key := range catalog.Messages {
		if name, ok := strings.CutPrefix(key, "widget."); ok {
			out[name] = catalog.Text(key, locale)
		}
	}
	return out
}

// on reports whether the channel takes submissions: not disabled in the
// config, and not switched off in Settings › Channels.
func (s Server) on(spec ChannelSpec) bool {
	if spec.Disabled {
		return false
	}
	p, err := board.Store{Home: s.Home, Role: "cloud"}.Project(spec.Project)
	if err != nil {
		return false
	}
	for _, ch := range p.Channels {
		if ch.ID == spec.ID {
			return ch.On
		}
	}
	return true
}

func (s Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log.Printf(format, args...)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// HTTPServer wraps the handler with timeouts fit for the open internet.
func (s Server) HTTPServer(addr string) *http.Server {
	return &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
}
