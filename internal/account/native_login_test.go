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

func TestNativeAuthenticationCreatesKeyWithoutDeviceConsent(t *testing.T) {
	for _, provider := range []string{"google", "github", "email"} {
		t.Run(provider, func(t *testing.T) {
			var challenge string
			var registered atomic.Bool
			var exchanges atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if r.Method == "POST" {
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
				}
				switch r.URL.Path {
				case "/v1/auth/oauth/start":
					if body["provider"] != provider {
						t.Error("wrong provider")
					}
					ioJSON(w, `{"data":{"attempt_id":"attempt"}}`)
				case "/auth/v1/otp":
					if provider != "email" || body["email"] != "person@example.com" || body["create_user"] != true || r.Header.Get("apikey") != "sb_publishable_test" {
						t.Error("invalid OTP request")
					}
					ioJSON(w, "{}")
				case "/auth/v1/token":
					exchanges.Add(1)
					verifier, _ := body["code_verifier"].(string)
					hash := sha256.Sum256([]byte(verifier))
					if body["auth_code"] != "test-code" || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge || r.URL.Query().Get("grant_type") != "pkce" {
						t.Error("invalid PKCE exchange")
					}
					ioJSON(w, `{"access_token":"supabase-secret","refresh_token":"unused-refresh"}`)
				case "/auth/v1/verify":
					exchanges.Add(1)
					if body["email"] != "person@example.com" || body["type"] != "email" {
						t.Error("invalid OTP verification")
					}
					if body["token"] == "000000" {
						http.Error(w, "invalid", 400)
						return
					}
					ioJSON(w, `{"access_token":"supabase-secret"}`)
				case "/v1/auth/session":
					if r.Header.Get("Authorization") != "Bearer supabase-secret" {
						t.Error("wrong session")
					}
					if provider != "email" && body["attempt_id"] != "attempt" {
						t.Error("missing audit attempt")
					}
					registered.Store(true)
					ioJSON(w, "{}")
				case "/v1/organizations":
					if !registered.Load() {
						t.Error("organization before registration")
					}
					ioJSON(w, `{"data":[{"id":"org-1","name":"First"},{"id":"org-2","name":"Second"}]}`)
				case "/v1/organizations/org-1/api-keys":
					if r.Method != "POST" || body["name"] != "Fennec iPhone" || r.Header.Get("Authorization") != "Bearer supabase-secret" {
						t.Error("invalid key creation")
					}
					scopes, _ := body["scopes"].([]any)
					if len(scopes) == 0 || body["expires_at"] == nil {
						t.Error("missing scoped expiry")
					}
					ioJSON(w, `{"data":{"plaintext_key":"runta-key","api_key":{"id":"key-id"}}}`)
				default:
					t.Errorf("unexpected request (device grants and hosted pages are forbidden): %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &memoryCredentials{}
			m := New(ctx, store, server.URL)
			m.http = server.Client()
			cfg := NativeOAuthConfig{AuthURL: server.URL, BackendURL: server.URL, PublishableKey: "sb_publishable_test"}
			var state Snapshot
			var err error
			if provider == "email" {
				state, err = m.LoginEmail("person@example.com", cfg)
				if err != nil {
					t.Fatal(err)
				}
				state = waitNative(t, m, func(s Snapshot) bool { return s.EmailCode })
				if state.URL != "" || m.CompleteNative(NativeCallbackURL+"?code=wrong", state.Epoch) {
					t.Fatal("email opened/accepted browser callback")
				}
				if m.VerifyEmail("123456", state.Epoch+1) {
					t.Fatal("stale OTP accepted")
				}
				if !m.VerifyEmail("000000", state.Epoch) {
					t.Fatal("OTP rejected")
				}
				state = waitNative(t, m, func(s Snapshot) bool { return s.Error == "invalid_email_code" })
				if !m.VerifyEmail("123456", state.Epoch) {
					t.Fatal("retry rejected")
				}
			} else {
				state, err = m.LoginNative(provider, cfg)
				if err != nil {
					t.Fatal(err)
				}
				state = waitNative(t, m, func(s Snapshot) bool { return s.URL != "" })
				u, _ := url.Parse(state.URL)
				q := u.Query()
				challenge = q.Get("code_challenge")
				if u.Path != "/auth/v1/authorize" || q.Get("provider") != provider || q.Get("redirect_to") != NativeCallbackURL || q.Get("code_challenge_method") != "s256" {
					t.Fatal("wrong provider URL")
				}
				if m.CompleteNative(NativeCallbackURL+"?code=test-code", state.Epoch+1) {
					t.Fatal("stale callback accepted")
				}
				if !m.CompleteNative(NativeCallbackURL+"?code=test-code", state.Epoch) {
					t.Fatal("callback rejected")
				}
				if m.CompleteNative(NativeCallbackURL+"?code=test-code", state.Epoch) {
					t.Fatal("duplicate accepted")
				}
			}
			state = waitNative(t, m, func(s Snapshot) bool { return s.Status == "authorized" })
			saved, _ := store.Read(ctx)
			if saved != "runta-key" {
				t.Fatal("wrong persisted key")
			}
			raw, _ := json.Marshal(state)
			for _, secret := range []string{"supabase-secret", "runta-key", "unused-refresh", "person@example.com"} {
				if strings.Contains(string(raw), secret) {
					t.Fatal("secret in snapshot")
				}
			}
			want := int32(1)
			if provider == "email" {
				want = 2
			}
			if exchanges.Load() != want {
				t.Fatal("wrong exchange count")
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
			if token, _ := m.Token(); token != "" {
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
	if m.Snapshot().Status != "authorizing" {
		t.Fatal("stale exchange replaced pending login")
	}
	if token, _ := m.Token(); token != "" {
		t.Fatal("stale token retained")
	}
}

// A failed Keychain write must not leave the newly minted API key active.
type rejectingNativeCredentials struct{ memoryCredentials }

func (*rejectingNativeCredentials) Write(context.Context, string) error { return context.Canceled }

func TestNativeKeyRevokedWhenStorageFails(t *testing.T) {
	revoked := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/oauth/start":
			ioJSON(w, `{"data":{"attempt_id":"attempt"}}`)
		case "/auth/v1/token":
			ioJSON(w, `{"access_token":"temporary"}`)
		case "/v1/auth/session":
			ioJSON(w, `{}`)
		case "/v1/organizations":
			ioJSON(w, `{"data":[{"id":"org-1"}]}`)
		case "/v1/organizations/org-1/api-keys":
			ioJSON(w, `{"data":{"plaintext_key":"issued-key","api_key":{"id":"key-1"}}}`)
		case "/v1/organizations/org-1/api-keys/key-1":
			if r.Method != "DELETE" || r.Header.Get("Authorization") != "Bearer temporary" {
				t.Error("invalid revocation")
			}
			revoked <- struct{}{}
			ioJSON(w, `{}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, &rejectingNativeCredentials{}, server.URL)
	m.http = server.Client()
	_, err := m.LoginNative("google", NativeOAuthConfig{AuthURL: server.URL, BackendURL: server.URL, PublishableKey: "sb_publishable_test"})
	if err != nil {
		t.Fatal(err)
	}
	state := waitNative(t, m, func(s Snapshot) bool { return s.URL != "" })
	if !m.CompleteNative(NativeCallbackURL+"?code=code", state.Epoch) {
		t.Fatal("callback rejected")
	}
	waitNative(t, m, func(s Snapshot) bool { return s.Error == "credential_save_failed" })
	select {
	case <-revoked:
	case <-time.After(3 * time.Second):
		t.Fatal("issued key was not revoked")
	}
	if token, _ := m.Token(); token != "" {
		t.Fatal("failed credential published")
	}
}
