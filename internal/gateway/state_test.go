package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeValidatesLocalResponse(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		ok     bool
	}{
		{404, `{"error":{"type":"not_found_error"}}`, true},
		{200, `{"error":{"type":"not_found_error"}}`, false},
		{401, `{"error":{"type":"authentication_error"}}`, false},
		{429, `{}`, false}, {404, `<html>proxy</html>`, false}, {404, `{"error":{"type":"other"}}`, false},
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/_fennec/liveness" {
				t.Error(r.URL.Path)
			}
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}))
		err := Probe(context.Background(), s.Client(), s.URL)
		s.Close()
		if (err == nil) != tc.ok {
			t.Fatalf("status %d body %s: %v", tc.status, tc.body, err)
		}
	}
}
func TestStaleHealthIsUnknown(t *testing.T) {
	home := t.TempDir()
	if err := writeState(home, State{Status: "healthy"}); err != nil {
		t.Fatal(err)
	}
	if got := ReadState(home); got.Status != "healthy" {
		t.Fatal(got)
	}
	old := []byte(`{"status":"healthy","updated":"2000-01-01T00:00:00Z"}`)
	if err := atomicWrite(statePath(home), old, 0600); err != nil {
		t.Fatal(err)
	}
	if got := ReadState(home); got.Status != "unknown" {
		t.Fatal(got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if Probe(ctx, &http.Client{}, "http://127.0.0.1:1") == nil {
		t.Fatal("expired probe accepted")
	}
}
