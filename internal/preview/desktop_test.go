package preview

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func desktopRequest(t *testing.T, d *Desktop, url string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest("GET", url, nil)
	for k, v := range headers {
		request.Header.Set(k, v)
	}
	response := httptest.NewRecorder()
	d.Handler().ServeHTTP(response, request)
	return response
}

func TestDesktopServesPreviewsFromTheCloud(t *testing.T) {
	store, workspace := fixture(t)
	os.WriteFile(filepath.Join(workspace, "chart.png"), []byte("\x89PNG\r\n\x1a\nimage"), 0600)
	os.WriteFile(filepath.Join(workspace, "report.html"), []byte("<html><script>draw()</script></html>"), 0600)
	image, err := store.Register("amber-cedar", "chart.png", "Chart")
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.Register("amber-cedar", "report.html", "Report")
	if err != nil {
		t.Fatal(err)
	}
	d := &Desktop{Dial: store.Dial}
	base := "http://localhost:47823/p/amber-cedar/"

	response := desktopRequest(t, d, base+image.Ref, map[string]string{"Sec-Fetch-Site": "none"})
	if response.Code != 200 || !strings.HasPrefix(response.Body.String(), "\x89PNG") {
		t.Fatal(response.Code, response.Body.String())
	}
	response = desktopRequest(t, d, base+page.Ref, nil)
	csp := response.Header().Get("Content-Security-Policy")
	if response.Code != 200 || response.Header().Get("Content-Disposition") != "inline" ||
		!strings.Contains(csp, "sandbox allow-scripts") || strings.Contains(csp, "allow-same-origin") || !strings.Contains(response.Body.String(), "draw()") {
		t.Fatal(response.Code, response.Header(), response.Body.String())
	}
	// The link in Markup is what the Mac serves.
	if !strings.Contains(Markup("amber-cedar", image.Ref, "Chart"), base+image.Ref) {
		t.Fatal("Markup does not point at the desktop server")
	}
}

func TestDesktopRefusesOtherSitesAndHosts(t *testing.T) {
	store, workspace := fixture(t)
	os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("secret"), 0600)
	m, _ := store.Register("amber-cedar", "a.txt", "")
	d := &Desktop{Dial: store.Dial}
	good := "http://localhost:47823/p/amber-cedar/" + m.Ref
	for name, c := range map[string]struct {
		url     string
		headers map[string]string
		code    int
	}{
		"rebinding":     {"http://evil.example:47823/p/amber-cedar/" + m.Ref, nil, 403},
		"cross-site":    {good, map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		"other origin":  {good, map[string]string{"Origin": "https://evil.example"}, 403},
		"other session": {"http://localhost:47823/p/other-place/" + m.Ref, nil, 404},
		"traversal":     {"http://localhost:47823/p/amber-cedar/../secret", nil, 404},
		"bad ref":       {"http://localhost:47823/p/amber-cedar/pv_x", nil, 404},
		"root":          {"http://localhost:47823/", nil, 404},
	} {
		if response := desktopRequest(t, d, c.url, c.headers); response.Code != c.code {
			t.Errorf("%s: %d, want %d", name, response.Code, c.code)
		}
	}
}

func TestDesktopSaysWhenTheCloudIsAway(t *testing.T) {
	d := &Desktop{Dial: func(context.Context) (net.Conn, error) { return nil, errors.New("connecting") }}
	response := desktopRequest(t, d, "http://localhost:47823/p/amber-cedar/pv_"+strings.Repeat("a", 32), nil)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "connecting") {
		t.Fatal(response.Code, response.Body.String())
	}
}
