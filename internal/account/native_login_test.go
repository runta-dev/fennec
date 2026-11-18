package account

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeepLinkRoutes(t *testing.T) {
	for raw, want := range map[string]string{
		"fennec://auth/callback?code=abc":                                     "auth/callback",
		"fennec://auth/callback?error=access_denied&error_description=Denied": "auth/callback",
		"fennec://pair#code=abc&api=https%3A%2F%2Fapi.runta.com":              "pair",
		"fennec://auth/callback":                                              "", "fennec://auth/callback?code=a&code=b": "",
		"fennec://auth/callback?code=a&error=denied": "", "fennec://auth/callback?code=a&error_code=bad": "",
		"fennec://auth/callback?code=a&api=https://evil.example": "",
		"fennec://auth/callback?code=%GG":                        "", "fennec://auth/callback?code=a#access_token=b": "",
		"fennec://auth/callback/?code=a": "", "fennec://auth/%63allback?code=a": "",
		"fennec://user@auth/callback?code=a": "", "fennec://auth:123/callback?code=a": "",
		"fennec://pair#code=a&code=b&api=https://api.runta.com": "", "fennec://pair?code=a&api=b": "",
		"fennec://auth/complete?state=a": "", "https://auth/callback?code=a": "",
	} {
		if got := DeepLinkRoute(raw); got != want {
			t.Errorf("%s: got %q want %q", raw, got, want)
		}
	}
}

func waitNative(t *testing.T, m *Manager, predicate func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		changed := m.Changes()
		s := m.Snapshot()
		if predicate(s) {
			return s
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("login did not progress: %+v", s)
		}
	}
}

func TestNativePKCEConsentAndCredential(t *testing.T) {
	for _, provider := range []string{"google", "github"} {
		t.Run(provider, func(t *testing.T) {
			var challenge string
			var registered, approved atomic.Bool
			var exchanges atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]string
				if r.Method == "POST" {
					if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
						t.Error(e)
					}
				}
				switch r.URL.Path {
				case "/v1/auth/oauth/start":
					if body["provider"] != provider {
						t.Error(body)
					}
					ioJSON(w, `{"data":{"attempt_id":"attempt-1"}}`)
				case "/v2/auth/device/authorization":
					ioJSON(w, `{"data":{"device_code":"device-secret","user_code":"ABCD-EFGH"}}`)
				case "/auth/v1/token":
					exchanges.Add(1)
					hash := sha256.Sum256([]byte(body["code_verifier"]))
					if body["auth_code"] != "test-code" || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge || r.URL.Query().Get("grant_type") != "pkce" || r.Header.Get("apikey") != "sb_publishable_test" {
						t.Error("invalid PKCE exchange")
					}
					ioJSON(w, `{"access_token":"supabase-secret","refresh_token":"unused-refresh-secret"}`)
				case "/v1/auth/session":
					if body["attempt_id"] != "attempt-1" || r.Header.Get("Authorization") != "Bearer supabase-secret" {
						t.Error("invalid session registration")
					}
					registered.Store(true)
					ioJSON(w, `{"data":{}}`)
				case "/v1/auth/device/authorization":
					if !registered.Load() || r.URL.Query().Get("code") != "ABCD-EFGH" {
						t.Error("consent before registration")
					}
					ioJSON(w, `{"data":{"client_name":"Fennec","access":"Full access","credential_expires_in_days":30,"organizations":[{"id":"org-1","name":"First"},{"id":"org-2","name":"Second"}]}}`)
				case "/v1/auth/device/authorization/approve":
					if body["organization_id"] != "org-2" || body["code"] != "ABCD-EFGH" || r.Header.Get("Authorization") != "Bearer supabase-secret" {
						t.Error("wrong approval")
					}
					approved.Store(true)
					ioJSON(w, `{"data":{}}`)
				case "/v2/auth/device/token":
					if !approved.Load() || body["device_code"] != "device-secret" {
						t.Error("token before consent")
					}
					ioJSON(w, `{"access_token":"runta-device-secret"}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &memoryCredentials{}
			m := New(ctx, store, server.URL)
			m.http = server.Client()
			s, e := m.LoginNative(provider, NativeOAuthConfig{AuthURL: server.URL, BackendURL: server.URL, PublishableKey: "sb_publishable_test"})
			if e != nil {
				t.Fatal(e)
			}
			s = waitNative(t, m, func(s Snapshot) bool { return s.URL != "" })
			u, _ := url.Parse(s.URL)
			q := u.Query()
			challenge = q.Get("code_challenge")
			if q.Get("redirect_to") != NativeCallbackURL || q.Get("code_challenge_method") != "s256" || challenge == "" || q.Get("provider") != provider {
				t.Fatal("invalid authorization URL")
			}
			if m.CompleteNative(NativeCallbackURL+"?code=test-code", s.Epoch+1) {
				t.Fatal("accepted stale generation")
			}
			if !m.CompleteNative(NativeCallbackURL+"?code=test-code", s.Epoch) {
				t.Fatal("rejected callback")
			}
			if m.CompleteNative(NativeCallbackURL+"?code=test-code", s.Epoch) {
				t.Fatal("accepted duplicate callback")
			}
			s = waitNative(t, m, func(s Snapshot) bool { return s.Consent != nil })
			data, _ := json.Marshal(s)
			for _, secret := range []string{"supabase-secret", "device-secret", "unused-refresh-secret", "test-code"} {
				if strings.Contains(string(data), secret) {
					t.Fatal("snapshot contains secret")
				}
			}
			if token, _ := m.Token(); token != "" {
				t.Fatal("signed in before consent")
			}
			if m.ApproveNative("attacker-org", s.Epoch) {
				t.Fatal("accepted unknown organization")
			}
			if m.ApproveNative("org-2", s.Epoch+1) {
				t.Fatal("accepted stale consent")
			}
			if !m.ApproveNative("org-2", s.Epoch) {
				t.Fatal("rejected consent")
			}
			waitNative(t, m, func(s Snapshot) bool { return s.Status == "authorized" })
			token, e := store.Read(ctx)
			if e != nil || token != "runta-device-secret" {
				t.Fatal("wrong persisted credential")
			}
			if exchanges.Load() != 1 {
				t.Fatal("code redeemed more than once")
			}
		})
	}
}

func ioJSON(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(s))
}

func TestNativeCancelAndRegistrationFailure(t *testing.T) {
	for _, scenario := range []string{"cancel", "expired", "denied", "registration-failed"} {
		t.Run(scenario, func(t *testing.T) {
			var exchanges atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/auth/oauth/start":
					ioJSON(w, `{"data":{"attempt_id":"attempt"}}`)
				case "/v2/auth/device/authorization":
					ioJSON(w, `{"data":{"device_code":"device","user_code":"ABCD-EFGH"}}`)
				case "/auth/v1/token":
					exchanges.Add(1)
					ioJSON(w, `{"access_token":"temporary"}`)
				case "/v1/auth/session":
					http.Error(w, "registration refused", http.StatusForbidden)
				default:
					t.Error("unexpected request after rejection")
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := New(ctx, &memoryCredentials{}, server.URL)
			m.http = server.Client()
			_, e := m.LoginNative("google", NativeOAuthConfig{AuthURL: server.URL, BackendURL: server.URL, PublishableKey: "sb_publishable_test"})
			if e != nil {
				t.Fatal(e)
			}
			s := waitNative(t, m, func(s Snapshot) bool { return s.URL != "" })
			switch scenario {
			case "cancel":
				if e := m.CancelAuthorization(ctx); e != nil {
					t.Fatal(e)
				}
				if m.CompleteNative(NativeCallbackURL+"?code=x", s.Epoch) {
					t.Fatal("accepted cancelled attempt")
				}
			case "expired":
				m.mu.Lock()
				m.native.expires = time.Now().Add(-time.Second)
				m.mu.Unlock()
				if m.CompleteNative(NativeCallbackURL+"?code=x", s.Epoch) {
					t.Fatal("accepted expired attempt")
				}
			case "denied":
				if !m.CompleteNative(NativeCallbackURL+"?error=access_denied", s.Epoch) {
					t.Fatal("rejected error")
				}
				if m.Snapshot().Error != "access_denied" {
					t.Fatal(m.Snapshot())
				}
			case "registration-failed":
				if !m.CompleteNative(NativeCallbackURL+"?code=x", s.Epoch) {
					t.Fatal("rejected callback")
				}
				waitNative(t, m, func(s Snapshot) bool { return s.Error != "" })
			}
			if scenario != "registration-failed" && exchanges.Load() != 0 {
				t.Fatal("unexpected code exchange")
			}
			if token, _ := m.Token(); token != "" || m.Snapshot().Consent != nil {
				t.Fatal("failed login retained authorization")
			}
		})
	}
}

func TestNativeLateExchangeCannotReplaceNewAttempt(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/oauth/start":
			ioJSON(w, `{"data":{"attempt_id":"attempt"}}`)
		case "/v2/auth/device/authorization":
			ioJSON(w, `{"data":{"device_code":"device","user_code":"ABCD-EFGH"}}`)
		case "/auth/v1/token":
			close(entered)
			<-release
			ioJSON(w, `{"access_token":"stale-secret"}`)
		default:
			t.Error("stale login continued after cancellation")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, &memoryCredentials{}, server.URL)
	m.http = server.Client()
	cfg := NativeOAuthConfig{AuthURL: server.URL, BackendURL: server.URL, PublishableKey: "sb_publishable_test"}
	if _, err := m.LoginNative("google", cfg); err != nil {
		t.Fatal(err)
	}
	old := waitNative(t, m, func(s Snapshot) bool { return s.URL != "" })
	if !m.CompleteNative(NativeCallbackURL+"?code=old", old.Epoch) {
		t.Fatal("callback rejected")
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("no exchange")
	}
	if err := m.CancelAuthorization(ctx); err != nil {
		close(release)
		t.Fatal(err)
	}
	if _, err := m.LoginNative("github", cfg); err != nil {
		close(release)
		t.Fatal(err)
	}
	current := waitNative(t, m, func(s Snapshot) bool { return s.URL != "" })
	close(release)
	if current.Epoch == old.Epoch || m.CompleteNative(NativeCallbackURL+"?code=old", old.Epoch) {
		t.Fatal("accepted stale browser generation")
	}
	if m.Snapshot().Status != "authorizing" || m.Snapshot().Consent != nil {
		t.Fatal("stale exchange replaced pending login")
	}
	if token, _ := m.Token(); token != "" {
		t.Fatal("stale token retained")
	}
}
