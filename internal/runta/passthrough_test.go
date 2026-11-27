package runta

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestEnsureTLSPassthroughKeepsExistingHostsAndRetriesConflict(t *testing.T) {
	revision, patches := 5, 0
	hosts := []string{"api.example.com"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v2/runtimes/rt-1":
			encoded, _ := json.Marshal(hosts)
			fmt.Fprintf(w, `{"data":{"id":"rt-1","revision":%d,"tls_passthrough_hosts":%s}}`, revision, encoded)
		case r.Method == "PATCH" && r.URL.Path == "/v2/runtimes/rt-1":
			patches++
			if patches == 1 {
				// Another writer moved the revision between GET and PATCH.
				revision = 6
				w.WriteHeader(http.StatusConflict)
				fmt.Fprint(w, `{"error":{"code":"failed_precondition","message":"revision"}}`)
				return
			}
			if got := r.URL.Query().Get("expected_revision"); got != "6" {
				t.Fatalf("expected revision 6 after the conflict, got %s", got)
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 {
				t.Fatalf("patch must only touch tls_passthrough_hosts: %v", body)
			}
			if err := json.Unmarshal(body["tls_passthrough_hosts"], &hosts); err != nil {
				t.Fatal(err)
			}
			revision++
			fmt.Fprintf(w, `{"data":{"id":"rt-1","revision":%d}}`, revision)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}

	changed, err := c.EnsureTLSPassthrough(context.Background(), "rt-1", TailscaleTLSPassthroughHosts)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if want := []string{"api.example.com", "*.tailscale.com"}; !slices.Equal(hosts, want) {
		t.Fatalf("hosts = %v, want %v", hosts, want)
	}

	// Already present (case-insensitively): no second PATCH.
	hosts = []string{"*.Tailscale.com"}
	changed, err = c.EnsureTLSPassthrough(context.Background(), "rt-1", TailscaleTLSPassthroughHosts)
	if err != nil || changed || patches != 2 {
		t.Fatalf("changed=%v err=%v patches=%d", changed, err, patches)
	}
}
