package account

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// appleServer fakes Supabase Auth and the Runta backend for Sign in with Apple.
// start is the oauth/start status (200 records attempt "attempt"); token is the
// id_token grant's status.
func appleServer(t *testing.T, start, token int) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var seen []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Method == "POST" || r.Method == "PUT" {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
		}
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.Query().Get("grant_type"))
		mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/auth/oauth/start":
			if body["provider"] != "apple" {
				t.Errorf("wrong provider: %v", body)
			}
			if start != 200 {
				http.Error(w, "unknown provider", start)
				return
			}
			ioJSON(w, `{"data":{"attempt_id":"attempt"}}`)
		case "POST /auth/v1/token":
			if r.Header.Get("apikey") != "sb_publishable_test" {
				t.Error("missing publishable key")
			}
			switch r.URL.Query().Get("grant_type") {
			case "id_token":
				if body["provider"] != "apple" || body["id_token"] != "apple-id-token" || body["nonce"] != "raw-nonce" {
					t.Errorf("invalid id_token grant: %v", body)
				}
				if token != 200 {
					http.Error(w, `{"msg":"Provider not enabled"}`, token)
					return
				}
				ioJSON(w, `{"access_token":"supabase-secret","refresh_token":"apple-refresh"}`)
			case "refresh_token":
				if body["refresh_token"] != "apple-refresh" {
					t.Error("wrong refresh token")
				}
				ioJSON(w, `{"access_token":"named-secret"}`)
			default:
				t.Errorf("unexpected grant %s", r.URL.RawQuery)
			}
		case "PUT /auth/v1/user":
			data, _ := body["data"].(map[string]any)
			if r.Header.Get("Authorization") != "Bearer supabase-secret" || data["full_name"] != "Ada Lovelace" {
				t.Errorf("invalid name update: %v", body)
			}
			ioJSON(w, "{}")
		case "POST /v1/auth/session":
			want := "attempt"
			if start != 200 {
				want = ""
			}
			if got, _ := body["attempt_id"].(string); got != want {
				t.Errorf("attempt_id %q, want %q", got, want)
			}
			ioJSON(w, "{}")
		case "GET /v1/organizations":
			ioJSON(w, `{"data":[{"id":"org-1","name":"First"}]}`)
		case "GET /v1/organizations/org-1/api-keys":
			ioJSON(w, `{"data":[]}`)
		case "POST /v1/organizations/org-1/api-keys":
			ioJSON(w, `{"data":{"plaintext_key":"runta-key","api_key":{"id":"key-id"}}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	return server, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string{}, seen...) }
}

func appleManager(t *testing.T, server *httptest.Server) (*Manager, *memoryCredentials, NativeOAuthConfig) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store := &memoryCredentials{}
	m := New(ctx, store, server.URL)
	m.http = server.Client()
	return m, store, NativeOAuthConfig{AuthURL: server.URL, BackendURL: server.URL, DashboardURL: server.URL, PublishableKey: "sb_publishable_test", KeyName: "Fennec iPhone (ab12)"}
}

func TestAppleSignInCreatesKey(t *testing.T) {
	for _, tc := range []struct {
		name, fullName string
		start          int
		session        string
	}{
		{"first authorization with name", "Ada Lovelace", 200, "named-secret"},
		{"returning, no name", "", 200, "supabase-secret"},
		{"Runta without the apple provider", "", 400, "supabase-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, seen := appleServer(t, tc.start, 200)
			defer server.Close()
			m, store, cfg := appleManager(t, server)
			state, err := m.LoginApple(cfg)
			if err != nil || state.Status != "authorizing" {
				t.Fatalf("start: %v %+v", err, state)
			}
			if m.CompleteNative(NativeCallbackURL+"?code=x", state.Epoch) {
				t.Fatal("browser callback accepted for an Apple attempt")
			}
			if m.VerifyEmail("123456", state.Epoch) {
				t.Fatal("email code accepted for an Apple attempt")
			}
			if m.CompleteApple("apple-id-token", "raw-nonce", tc.fullName, state.Epoch+1) {
				t.Fatal("stale epoch accepted")
			}
			if m.CompleteApple("", "raw-nonce", "", state.Epoch) || m.CompleteApple("apple-id-token", "", "", state.Epoch) {
				t.Fatal("empty token or nonce accepted")
			}
			if !m.CompleteApple("apple-id-token", "raw-nonce", tc.fullName, state.Epoch) {
				t.Fatal("token rejected")
			}
			if m.CompleteApple("apple-id-token", "raw-nonce", tc.fullName, state.Epoch) {
				t.Fatal("duplicate accepted")
			}
			state = waitNative(t, m, func(s Snapshot) bool { return s.Status == "authorized" || s.Error != "" })
			if state.Status != "authorized" || state.URL != "" {
				t.Fatalf("not signed in: %+v", state)
			}
			if saved, _ := store.Read(context.Background()); saved != "runta-key" {
				t.Fatal("wrong persisted key")
			}
			raw, _ := json.Marshal(state)
			for _, secret := range []string{"apple-id-token", "raw-nonce", "supabase-secret", "named-secret", "runta-key"} {
				if strings.Contains(string(raw), secret) {
					t.Fatal("secret in snapshot")
				}
			}
			named := strings.Contains(strings.Join(seen(), ","), "PUT /auth/v1/user")
			if named != (tc.fullName != "") {
				t.Fatalf("name update %v for %q: %v", named, tc.fullName, seen())
			}
		})
	}
}

func TestAppleSignInRefusedToken(t *testing.T) {
	server, seen := appleServer(t, 200, http.StatusUnprocessableEntity)
	defer server.Close()
	m, store, cfg := appleManager(t, server)
	state, _ := m.LoginApple(cfg)
	if !m.CompleteApple("apple-id-token", "raw-nonce", "", state.Epoch) {
		t.Fatal("token rejected locally")
	}
	state = waitNative(t, m, func(s Snapshot) bool { return s.Status == "signed_out" })
	if state.Error != "authorization_failed" || !strings.Contains(state.Detail, "HTTP 422") {
		t.Fatalf("wrong failure: %+v", state)
	}
	if saved, _ := store.Read(context.Background()); saved != "" {
		t.Fatal("key saved after a refused token")
	}
	if strings.Contains(strings.Join(seen(), ","), "/v1/auth/session") {
		t.Fatal("session registered after a refused token")
	}
}

func TestAppleSignInCancelled(t *testing.T) {
	server, _ := appleServer(t, 200, 200)
	defer server.Close()
	m, _, cfg := appleManager(t, server)
	state, _ := m.LoginApple(cfg)
	if err := m.CancelAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.CompleteApple("apple-id-token", "raw-nonce", "", state.Epoch) {
		t.Fatal("token accepted after cancel")
	}
}
