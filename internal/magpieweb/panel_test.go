package magpieweb

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPanelAuthenticationForwardingAndDisable(t *testing.T) {
	var enabled atomic.Bool
	enabled.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app.js" {
			w.Header().Set("Cache-Control", "max-age=31536000, immutable")
		}
		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.Write([]byte(r.Method + " " + r.URL.RequestURI()))
	}))
	defer upstream.Close()
	address := strings.TrimPrefix(upstream.URL, "http://")
	local := &Local{}
	defer local.Close()
	link, err := local.URL(context.Background(), func(ctx context.Context) (net.Conn, error) { return (&net.Dialer{}).DialContext(ctx, "tcp", address) }, enabled.Load)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	origin := "http://" + u.Host
	get := func(c *http.Client, path string, want int) string {
		t.Helper()
		r, e := c.Get(path)
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		if r.StatusCode != want {
			t.Fatalf("status %d: %s", r.StatusCode, b)
		}
		return string(b)
	}
	get(http.DefaultClient, origin, 403)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if got := get(client, link, 200); got != "GET /?view=agents" {
		t.Fatal(got)
	}
	if got := get(client, origin+"/events?q=1", 200); got != "GET /events?q=1" {
		t.Fatal(got)
	}
	asset, err := client.Get(origin + "/app.js?v=hash")
	if err != nil {
		t.Fatal(err)
	}
	asset.Body.Close()
	if got := asset.Header.Values("Cache-Control"); len(got) != 1 || got[0] != "max-age=31536000, immutable" {
		t.Fatalf("proxy defeats browser asset caching: %v", got)
	}
	req, _ := http.NewRequest("POST", origin+"/settings", nil)
	req.Header.Set("Origin", "https://untrusted.example")
	r, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal(r.StatusCode)
	}
	req, _ = http.NewRequest("GET", origin, nil)
	req.Host = "untrusted.example"
	r, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal(r.StatusCode)
	}
	enabled.Store(false)
	get(client, origin, 403)
}

func TestCloudKeepsAccessKeyOnCloud(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config/fennec")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "gateway-enabled"), []byte("true\n"), 0600)
	var address string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, port, _ := net.SplitHostPort(address)
		cookie, err := r.Cookie("magpie_web_" + port)
		if err != nil || cookie.Value != "cloud-only-key" || r.Header.Get("Origin") != "http://"+address {
			t.Error("missing cloud authentication or origin")
		}
		w.Header().Set("Set-Cookie", "secret=cloud-only-key")
		w.Write([]byte("panel"))
	}))
	defer upstream.Close()
	address = strings.TrimPrefix(upstream.URL, "http://")
	cloud := &Cloud{address: address, key: "cloud-only-key"}
	conn, err := cloud.Dial(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequest("GET", "http://panel/", nil)
	req.Header.Set("Origin", "http://local")
	r, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.Header.Get("Set-Cookie") != "" {
		t.Fatal("cloud credential escaped")
	}
	os.WriteFile(filepath.Join(dir, "gateway-enabled"), []byte("false\n"), 0600)
	if _, err = cloud.Dial(context.Background(), home); err == nil {
		t.Fatal("disabled panel accepted")
	}
}
