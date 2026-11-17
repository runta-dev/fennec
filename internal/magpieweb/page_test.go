package magpieweb

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPanelResponsePreservesVersionedAssetsAndProtectsAPI(t *testing.T) {
	for _, tc := range []struct{ path, cache, want string }{
		{"/app.js?v=contenthash", "max-age=31536000, immutable", "max-age=31536000, immutable"},
		{"/plugins/library/i18n.js?v=hash", "max-age=31536000, immutable", "max-age=31536000, immutable"},
		{"/api/settings", "max-age=600", "no-store"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			u, _ := url.Parse("http://panel" + tc.path)
			r := &http.Response{StatusCode: 200, Header: make(http.Header), Request: &http.Request{URL: u}}
			r.Header.Set("Cache-Control", tc.cache)
			if err := preparePanelResponse(r); err != nil {
				t.Fatal(err)
			}
			if got := r.Header.Values("Cache-Control"); len(got) != 1 || got[0] != tc.want {
				t.Fatal(got)
			}
		})
	}
}

func TestCompressedPanelShowsLoadingBeforeBlockingScripts(t *testing.T) {
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	gz.Write([]byte("<!doctype html><html><head><script src=\"app.js?v=hash\"></script></head><body>Magpie</body></html>"))
	gz.Close()
	r := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(&compressed), Request: httptest.NewRequest("GET", "http://panel/?view=agents", nil)}
	r.Header.Set("Content-Type", "text/html; charset=utf-8")
	r.Header.Set("Content-Encoding", "gzip")
	r.Header.Set("ETag", "old")
	if err := preparePanelResponse(r); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Index(text, "data-fennec-panel-boot") > strings.Index(text, "app.js?v=hash") || !strings.Contains(text, "DOMContentLoaded") {
		t.Fatal("loading state must precede application scripts and end when they run")
	}
	if r.Header.Get("Content-Encoding") != "" || r.Header.Get("ETag") != "" || r.ContentLength != int64(len(body)) {
		t.Fatal("transformed representation metadata was retained")
	}
}
