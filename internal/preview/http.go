package preview

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"fennec/internal/sessionid"
)

// Handler has no browser/public listener. Only the authenticated resident
// SocketPreview route may call Dial. Do not expose this handler via ingress.
func (s Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/uploads/{upload}", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxBytes)
		defer r.Body.Close()
		result, err := s.UploadFile(r.PathValue("session"), r.PathValue("upload"), r.URL.Query().Get("name"), r.Body)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, result)
	})
	mux.HandleFunc("GET /v1/sessions/{session}/previews/{ref}", func(w http.ResponseWriter, r *http.Request) {
		metadata, err := s.Metadata(r.PathValue("session"), r.PathValue("ref"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, metadata)
	})
	mux.HandleFunc("GET /v1/sessions/{session}/previews/{ref}/content", func(w http.ResponseWriter, r *http.Request) {
		file, metadata, err := s.Open(r.PathValue("session"), r.PathValue("ref"))
		if err != nil {
			writeError(w, err)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", metadata.MIME)
		disposition := "inline"
		if metadata.Kind == "download" {
			disposition = "attachment"
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": metadata.Name}))
		w.Header().Set("ETag", `"`+metadata.SHA256+`"`)
		http.ServeContent(w, r, metadata.Name, metadata.CreatedAt, file)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// No ambient browser access, redirects, arbitrary target URLs or directory listing.
		if r.Header.Get("Origin") != "" || (r.Host != "preview.fennec" && r.Host != "") {
			http.Error(w, "preview access denied", http.StatusForbidden)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 4 || parts[0] != "v1" || parts[1] != "sessions" || !sessionid.Valid(parts[2]) {
			writeError(w, ErrInvalid)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	message := "preview unavailable"
	var limit *http.MaxBytesError
	switch {
	case errors.Is(err, ErrInvalid):
		code = http.StatusBadRequest
		message = "invalid preview request"
	case errors.Is(err, ErrTooLarge) || errors.As(err, &limit):
		code = http.StatusRequestEntityTooLarge
		message = "attachment exceeds 64 MB"
	case errors.Is(err, ErrConflict):
		code = http.StatusConflict
		message = "upload identifier conflict"
	case errors.Is(err, os.ErrNotExist):
		code = http.StatusNotFound
	}
	http.Error(w, message, code)
}

// Dial runs HTTP over an in-memory connection. There is no listening TCP port
// and closing/cancelling the resident stream tears down its HTTP connection.
func (s Store) Dial(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, server := net.Pipe()
	listener := &singleListener{conn: server, done: make(chan struct{})}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 3 * time.Minute, WriteTimeout: 3 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	srv.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			listener.Close()
		}
	}
	stop := context.AfterFunc(ctx, func() { srv.Close(); server.Close(); client.Close(); listener.Close() })
	go func() { defer stop(); srv.Serve(listener) }()
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
	conn := l.conn
	l.conn = nil
	l.mu.Unlock()
	if conn != nil {
		return conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *singleListener) Close() error { l.once.Do(func() { close(l.done) }); return nil }
func (l *singleListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "fennec-preview", Net: "private"}
}
