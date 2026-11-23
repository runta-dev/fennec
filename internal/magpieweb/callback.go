package magpieweb

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// callbackBridge keeps the vendor's fixed redirect URI, then hands the callback
// to the authenticated cloud sign-in API over the existing resident transport.
type callbackBridge struct {
	mu        sync.Mutex
	server    *http.Server
	flows     map[string]callbackFlow
	origin    string
	transport http.RoundTripper
	valid     func() bool
}
type callbackFlow struct {
	id      string
	expires time.Time
}

func (b *callbackBridge) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.server != nil {
		b.server.Close()
		b.server = nil
	}
	b.flows = nil
}
func (b *callbackBridge) observe(r *http.Response) error {
	if r.StatusCode != 200 || (r.Request.URL.Path != "/api/signin" && !strings.HasPrefix(r.Request.URL.Path, "/api/signin/")) {
		return nil
	}
	if r.Header.Get("Content-Encoding") != "" {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(data))
	if err != nil {
		return err
	}
	var st struct{ ID, URL, State string }
	if json.Unmarshal(data, &st) != nil || st.ID == "" {
		return nil
	}
	u, err := url.Parse(st.URL)
	if err != nil || u.Scheme != "https" || u.Host != "auth.openai.com" {
		return nil
	}
	if u.Query().Get("redirect_uri") != "http://localhost:1455/auth/callback" {
		return nil
	}
	state := u.Query().Get("state")
	if state == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if st.State != "waiting" {
		delete(b.flows, state)
		return nil
	}
	if b.server == nil {
		ln, err := net.Listen("tcp", "127.0.0.1:1455")
		// Preserve Magpie's manual paste fallback if another app owns the port.
		if err != nil {
			return nil
		}
		b.server = &http.Server{Handler: http.HandlerFunc(b.redirect), ReadHeaderTimeout: 5 * time.Second}
		go b.server.Serve(ln)
	}
	if b.flows == nil {
		b.flows = make(map[string]callbackFlow)
	}
	for key, flow := range b.flows {
		if time.Now().After(flow.expires) {
			delete(b.flows, key)
		}
	}
	if _, ok := b.flows[state]; !ok {
		b.flows[state] = callbackFlow{st.ID, time.Now().Add(10 * time.Minute)}
	}
	return nil
}
func (b *callbackBridge) flow(r *http.Request) (callbackFlow, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.flows[r.URL.Query().Get("state")]
	return f, ok && time.Now().Before(f.expires) && b.valid()
}
func (b *callbackBridge) redirect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != "GET" || r.URL.Path != "/auth/callback" || (r.Host != "localhost:1455" && r.Host != "127.0.0.1:1455") {
		http.NotFound(w, r)
		return
	}
	if _, ok := b.flow(r); !ok {
		http.Error(w, "Sign-in expired. Start again from Fennec Settings.", 403)
		return
	}
	http.Redirect(w, r, b.origin+"/auth/callback?"+r.URL.RawQuery, http.StatusSeeOther)
}
func (b *callbackBridge) complete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	f, ok := b.flow(r)
	if r.Method != "GET" || !ok {
		http.Error(w, "Sign-in expired. Start again from Fennec Settings.", 403)
		return
	}
	data, _ := json.Marshal(map[string]string{"URL": "http://localhost:1455/auth/callback?" + r.URL.RawQuery})
	req, err := http.NewRequestWithContext(r.Context(), "POST", "http://magpie/api/signin/"+url.PathEscape(f.id)+"/callback", bytes.NewReader(data))
	if err != nil {
		http.Error(w, "Could not complete sign-in", 502)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := b.transport.RoundTrip(req)
	if err != nil {
		http.Error(w, "Could not complete sign-in. Try again from the control panel.", 502)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		http.Error(w, "Sign-in was not accepted. Try again from the control panel.", 502)
		return
	}
	b.mu.Lock()
	delete(b.flows, r.URL.Query().Get("state"))
	b.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, "<!doctype html><title>Signed in</title><p>Signed in. You can close this tab and return to Magpie.</p>")
}
