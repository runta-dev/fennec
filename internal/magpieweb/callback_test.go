package magpieweb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCallbackThroughPanel(t *testing.T) {
	authorization := "https://auth.openai.com/oauth/authorize?" + url.Values{"redirect_uri": {"http://localhost:1455/auth/callback"}, "state": {"test-state"}}.Encode()
	completed := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/signin":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"id": "flow-id", "url": authorization, "state": "waiting"})
		case "/api/signin/flow-id/callback":
			var body struct{ URL string }
			json.NewDecoder(r.Body).Decode(&body)
			if r.Method != "POST" || body.URL != "http://localhost:1455/auth/callback?code=fake&state=test-state" {
				t.Errorf("unexpected callback request")
			}
			completed = true
			w.WriteHeader(204)
		}
	}))
	defer upstream.Close()
	local := &Local{}
	defer local.Close()
	link, err := local.URL(context.Background(), func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(upstream.URL, "http://"))
	}, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	r, err := client.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	base, _ := url.Parse(link)
	base.RawQuery = ""
	base.Path = "/api/signin"
	r, err = client.Post(base.String(), "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	// Simulate the cross-site redirect, without the panel's Strict cookie.
	base.Path = "/auth/callback"
	base.RawQuery = "code=fake&state=wrong"
	r, err = http.Get(base.String())
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 403 || completed {
		t.Fatal("unknown state accepted")
	}
	base.RawQuery = "code=fake&state=test-state"
	recorder := httptest.NewRecorder()
	local.callback.redirect(recorder, httptest.NewRequest("GET", "http://localhost:1455/auth/callback?"+base.RawQuery, nil))
	if recorder.Code != 303 || recorder.Header().Get("Location") != base.String() {
		t.Fatal("callback did not redirect to proxy")
	}
	r, err = http.Get(base.String())
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 || !completed {
		t.Fatal(fmt.Sprintf("callback failed: %d", r.StatusCode))
	}
	r, err = http.Get(base.String())
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal("replayed callback accepted")
	}
}
