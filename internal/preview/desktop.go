package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"fennec/internal/sessionid"
)

// Desktop serves the cloud computer's previews on the Mac's loopback at
// DesktopPort, so the link an agent prints (Markup) opens in the browser from
// any terminal. Each request goes to the cloud's private preview handler over
// the daemon's authenticated resident connection (Dial); no port is opened on
// the cloud computer and nothing is cached on the Mac.
//
// Only this Mac's browser is served: the Host must be the loopback origin
// (no DNS rebinding), and requests made by other web pages are refused.
type Desktop struct {
	// Dial opens the cloud computer's preview socket; it fails while the
	// cloud computer isn't connected.
	Dial func(context.Context) (net.Conn, error)
}

var desktopRef = regexp.MustCompile(`^pv_[a-f0-9]{32}$`)

// Serve listens on 127.0.0.1:DesktopPort until ctx ends, retrying while the
// port is taken (another Fennec, or a previous daemon still exiting).
func (d *Desktop) Serve(ctx context.Context) {
	server := &http.Server{Handler: d.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	context.AfterFunc(ctx, func() { server.Close() })
	warned := false
	for ctx.Err() == nil {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", DesktopPort))
		if err == nil {
			err = server.Serve(ln)
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
		}
		if !warned {
			log.Printf("preview server on port %d unavailable: %v", DesktopPort, err)
			warned = true
		}
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
		}
	}
}

func (d *Desktop) Handler() http.Handler {
	origin := fmt.Sprintf("http://localhost:%d", DesktopPort)
	hosts := map[string]bool{fmt.Sprintf("localhost:%d", DesktopPort): true, fmt.Sprintf("127.0.0.1:%d", DesktopPort): true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		site := r.Header.Get("Sec-Fetch-Site")
		if !hosts[r.Host] || (site != "" && site != "none" && site != "same-origin") ||
			(r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin && r.Header.Get("Origin") != "http://"+r.Host) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) != 3 || parts[0] != "p" || !sessionid.Valid(parts[1]) || !desktopRef.MatchString(parts[2]) {
			http.NotFound(w, r)
			return
		}
		d.proxy(w, r, parts[1], parts[2])
	})
}

func (d *Desktop) proxy(w http.ResponseWriter, r *http.Request, session, ref string) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return d.Dial(ctx) },
		DisableKeepAlives: true,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	upstream, _ := http.NewRequestWithContext(ctx, r.Method, "http://preview.fennec/v1/sessions/"+session+"/previews/"+ref+"/content", nil)
	upstream.Host = "preview.fennec"
	if value := r.Header.Get("Range"); value != "" {
		upstream.Header.Set("Range", value)
	}
	response, err := client.Do(upstream)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, unavailablePage)
		return
	}
	defer response.Body.Close()
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified", "Content-Disposition"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") && response.StatusCode < 300 {
		// A web page runs in a sandbox: its own opaque origin, no requests
		// back to this server or the Mac, inline scripts and styles allowed
		// so charts and reports render.
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-popups allow-downloads; default-src 'none'; "+
			"script-src 'unsafe-inline' data: https:; style-src 'unsafe-inline' data: https:; img-src data: blob: https:; font-src data: https:; media-src data: blob:")
	} else {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'; sandbox")
	}
	w.WriteHeader(response.StatusCode)
	if r.Method != http.MethodHead {
		io.Copy(w, response.Body)
	}
}

const unavailablePage = `<!doctype html><meta charset="utf-8"><title>Fennec</title>
<body style="font:15px -apple-system,system-ui,sans-serif;display:grid;place-items:center;height:90vh;color:#555">
<p>Fennec is connecting to your cloud computer. Reload this page in a moment.</p></body>`
