package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

var credentialTestNow = time.UnixMilli(1_800_000_000_000)

func claudeCredential(token string, expires int64) string {
	return claudeCredentialWith("a-"+token, "r-"+token, expires)
}

func claudeCredentialWith(access, refresh string, expires int64) string {
	b, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": access, "refreshToken": refresh, "expiresAt": expires, "scopes": []string{"user:inference"}, "subscriptionType": "team"}, "trustedDeviceToken": "device"})
	return string(b)
}

// signedOutClaude is the shape Claude Code leaves behind after a refresh
// fails because another computer rotated the shared refresh token.
const signedOutClaude = `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0,"refreshTokenExpiresAt":1796886417973,"scopes":["user:inference"],"subscriptionType":"team"},"trustedDeviceToken":"device"}`

// fakeAccounts stands in for the providers' account endpoints: the listed
// access tokens are accepted, everything else is rejected.
type fakeAccounts struct {
	mu       sync.Mutex
	accepted map[string]bool
	calls    []string
	accounts []string
}

func stubCredentialChecks(t *testing.T, accepted ...string) *fakeAccounts {
	t.Helper()
	fake := &fakeAccounts{accepted: map[string]bool{}}
	for _, token := range accepted {
		fake.accepted[token] = true
	}
	previousCheck, previousNow := nativeCredentialCheck, nativeCredentialNow
	nativeCredentialCheck = func(_ context.Context, provider, access, account string) bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.calls = append(fake.calls, provider+":"+access)
		fake.accounts = append(fake.accounts, account)
		return fake.accepted[access]
	}
	nativeCredentialNow = func() time.Time { return credentialTestNow }
	t.Cleanup(func() { nativeCredentialCheck, nativeCredentialNow = previousCheck, previousNow })
	return fake
}

func (f *fakeAccounts) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func encodeNative(v string) string { return base64.StdEncoding.EncodeToString([]byte(v)) }

// syncConflict syncs first, lets the cloud agent rewrite its copy, changes
// the Mac copy, and syncs again. It returns the cloud file and the result.
func syncConflict(t *testing.T, remote, name, first, cloud, mac string) ([]byte, ModelAccountsResult) {
	t.Helper()
	req := ModelAccountsRequest{Version: 1, Native: true, Files: map[string]string{name: encodeNative(first)}}
	if _, err := ApplyNativeAccounts(context.Background(), remote, req); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(remote, ".local/fennec/native", filepath.FromSlash(name))
	if err := os.WriteFile(target, []byte(cloud), 0o600); err != nil {
		t.Fatal(err)
	}
	req.Files[name] = encodeNative(mac)
	result, err := ApplyNativeAccounts(context.Background(), remote, req)
	if err != nil {
		t.Fatalf("a credential conflict must never fail preparation: %v", err)
	}
	raw, _ := os.ReadFile(target)
	return raw, result
}

func TestClaudeCredentialConflictPolicy(t *testing.T) {
	future, later := credentialTestNow.Add(4*time.Hour).UnixMilli(), credentialTestNow.Add(6*time.Hour).UnixMilli()
	expired := credentialTestNow.Add(-time.Hour).UnixMilli()
	for _, tc := range []struct {
		name      string
		cloud     string
		mac       string
		accepted  []string
		want      string // "cloud", "mac", or "deferred"
		maxChecks int
	}{
		// The incident: the cloud cleared its tokens after the Mac rotated the
		// shared refresh token. Decided without any network call.
		{"cloud signed out", signedOutClaude, claudeCredential("mac", future), nil, "mac", 0},
		{"mac signed out", claudeCredential("cloud", future), signedOutClaude, nil, "cloud", 0},
		{"same grant, mac later", claudeCredentialWith("a-c", "r-same", future), claudeCredentialWith("a-m", "r-same", later), nil, "mac", 0},
		{"same grant, cloud later", claudeCredentialWith("a-c", "r-same", later), claudeCredentialWith("a-m", "r-same", future), nil, "cloud", 0},
		{"same grant, same expiry, metadata differs", claudeCredentialWith("a-c", "r-same", future), `{"claudeAiOauth":{"accessToken":"a-m","refreshToken":"r-same","expiresAt":` + jsonNumber(future) + `,"subscriptionType":"max"}}`, nil, "cloud", 0},
		{"both valid, cloud newer", claudeCredential("cloud", later), claudeCredential("mac", future), []string{"a-cloud", "a-mac"}, "cloud", 2},
		{"both valid, equal expiry keeps cloud", claudeCredential("cloud", future), claudeCredential("mac", future), []string{"a-cloud", "a-mac"}, "cloud", 2},
		{"both valid, mac strictly newer", claudeCredential("cloud", future), claudeCredential("mac", later), []string{"a-cloud", "a-mac"}, "mac", 2},
		{"only cloud valid", claudeCredential("cloud", future), claudeCredential("mac", later), []string{"a-cloud"}, "cloud", 2},
		{"only mac valid", claudeCredential("cloud", later), claudeCredential("mac", future), []string{"a-mac"}, "mac", 2},
		{"mac expired, cloud valid", claudeCredential("cloud", future), claudeCredential("mac", expired), []string{"a-cloud", "a-mac"}, "cloud", 1},
		{"both expired", claudeCredential("cloud", expired), claudeCredential("mac", expired), []string{"a-cloud", "a-mac"}, "deferred", 0},
		{"offline", claudeCredential("cloud", future), claudeCredential("mac", later), nil, "deferred", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := stubCredentialChecks(t, tc.accepted...)
			kept, result := syncConflict(t, t.TempDir(), "claude/.credentials.json", claudeCredential("first", 1000), tc.cloud, tc.mac)
			want := map[string]string{"cloud": tc.cloud, "mac": tc.mac, "deferred": tc.cloud}[tc.want]
			if string(kept) != want {
				t.Fatalf("kept %s, want the %s copy", kept, tc.want)
			}
			if deferred := len(result.NativeDeferred) > 0; deferred != (tc.want == "deferred") {
				t.Fatalf("deferred = %v", result.NativeDeferred)
			}
			if fake.callCount() > tc.maxChecks {
				t.Fatalf("%d account checks, want at most %d: %v", fake.callCount(), tc.maxChecks, fake.calls)
			}
			for _, call := range fake.calls {
				if call != "claude:a-cloud" && call != "claude:a-mac" {
					t.Fatalf("unexpected check %s", call)
				}
			}
		})
	}
}

func jsonNumber(n int64) string { b, _ := json.Marshal(n); return string(b) }

// A deferred conflict leaves the baseline alone, so the next sync decides
// again, and a decided one updates it, so the next sync is quiet.
func TestCredentialConflictBaseline(t *testing.T) {
	expired := credentialTestNow.Add(-time.Hour).UnixMilli()
	future := credentialTestNow.Add(4 * time.Hour).UnixMilli()
	remote := t.TempDir()
	fake := stubCredentialChecks(t)
	cloud := claudeCredential("cloud", expired)
	kept, result := syncConflict(t, remote, "claude/.credentials.json", claudeCredential("first", 1000), cloud, claudeCredential("mac", expired))
	if string(kept) != cloud || !reflect.DeepEqual(result.NativeDeferred, []string{"claude/.credentials.json"}) {
		t.Fatalf("kept %s deferred %v", kept, result.NativeDeferred)
	}
	// The Mac agent refreshed itself; now its copy is confirmed and wins.
	fake.accepted["a-mac2"] = true
	req := ModelAccountsRequest{Version: 1, Native: true, Files: map[string]string{"claude/.credentials.json": encodeNative(claudeCredential("mac2", future))}}
	result, err := ApplyNativeAccounts(context.Background(), remote, req)
	if err != nil || len(result.NativeDeferred) != 0 {
		t.Fatalf("re-evaluation: %v %v", err, result.NativeDeferred)
	}
	target := filepath.Join(remote, ".local/fennec/native/claude/.credentials.json")
	if raw, _ := os.ReadFile(target); string(raw) != claudeCredential("mac2", future) {
		t.Fatalf("kept %s", raw)
	}
	checks := fake.callCount()
	if _, err := ApplyNativeAccounts(context.Background(), remote, req); err != nil || fake.callCount() != checks {
		t.Fatalf("an unchanged sync must not check again: %v", err)
	}
}

func TestCodexCredentialConflictPolicy(t *testing.T) {
	jwt := func(exp time.Time) string {
		payload, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
		return "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
	}
	auth := func(access, refresh, refreshed string) string {
		b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"access_token": access, "refresh_token": refresh, "account_id": "acct-1", "id_token": "id"}, "last_refresh": refreshed})
		return string(b)
	}
	soon, later := jwt(credentialTestNow.Add(2*time.Hour)), jwt(credentialTestNow.Add(9*time.Hour))
	t.Run("only the mac copy is accepted", func(t *testing.T) {
		fake := stubCredentialChecks(t, later)
		cloud, mac := auth(soon, "r-c", "2026-11-13T10:00:00Z"), auth(later, "r-m", "2026-11-12T10:00:00Z")
		kept, _ := syncConflict(t, t.TempDir(), "codex/auth.json", auth("x", "r-x", "2026-11-01T00:00:00Z"), cloud, mac)
		if string(kept) != mac {
			t.Fatalf("kept %s", kept)
		}
		for _, account := range fake.accounts {
			if account != "acct-1" {
				t.Fatalf("account header %q", account)
			}
		}
	})
	t.Run("same grant keeps the later access token", func(t *testing.T) {
		fake := stubCredentialChecks(t)
		cloud, mac := auth(later, "r-same", "2026-11-12T10:00:00Z"), auth(soon, "r-same", "2026-11-13T10:00:00Z")
		kept, _ := syncConflict(t, t.TempDir(), "codex/auth.json", auth("x", "r-x", "2026-11-01T00:00:00Z"), cloud, mac)
		if string(kept) != cloud || fake.callCount() != 0 {
			t.Fatalf("kept %s after %d checks", kept, fake.callCount())
		}
	})
	t.Run("API keys on both sides stay put", func(t *testing.T) {
		fake := stubCredentialChecks(t)
		cloud, mac := `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-c"}`, `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-m"}`
		kept, result := syncConflict(t, t.TempDir(), "codex/auth.json", `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-0"}`, cloud, mac)
		if string(kept) != cloud || len(result.NativeDeferred) != 1 || fake.callCount() != 0 {
			t.Fatalf("kept %s deferred %v checks %d", kept, result.NativeDeferred, fake.callCount())
		}
	})
}

func TestProviderCredentialConflictPolicy(t *testing.T) {
	future, later := credentialTestNow.Add(2*time.Hour).UnixMilli(), credentialTestNow.Add(5*time.Hour).UnixMilli()
	entry := func(access, refresh string, expires int64) map[string]any {
		return map[string]any{"type": "oauth", "access": access, "refresh": refresh, "expires": expires}
	}
	encode := func(v map[string]any) string { b, _ := json.Marshal(v); return string(b) }
	cloud := encode(map[string]any{
		"anthropic":    entry("a-c", "r-same", later),
		"openai-codex": entry("o-c", "r-oc", later),
		"groq":         map[string]any{"type": "api_key", "key": "k"},
		"cloud-only":   map[string]any{"type": "api_key", "key": "c"},
	})
	mac := encode(map[string]any{
		"anthropic":    entry("a-m", "r-same", future),
		"openai-codex": entry("o-m", "r-om", future),
		"groq":         map[string]any{"type": "api_key", "key": "k"},
		"mac-only":     map[string]any{"type": "api_key", "key": "m"},
	})
	for _, name := range []string{"pi/auth.json", "opencode/data/opencode/auth.json"} {
		t.Run(name, func(t *testing.T) {
			stubCredentialChecks(t, "o-m")
			kept, _ := syncConflict(t, t.TempDir(), name, `{}`, cloud, mac)
			var got map[string]map[string]any
			json.Unmarshal(kept, &got)
			if got["anthropic"]["access"] != "a-c" || got["openai-codex"]["access"] != "o-m" || got["groq"]["key"] != "k" || got["cloud-only"] == nil || got["mac-only"] == nil {
				t.Fatalf("per-provider merge: %s", kept)
			}
		})
	}
	t.Run("an undecidable provider keeps the whole cloud file", func(t *testing.T) {
		stubCredentialChecks(t)
		cloud := `{"groq":{"type":"api_key","key":"a"},"anthropic":{"type":"oauth","access":"a-c","refresh":"r","expires":1}}`
		mac := `{"groq":{"type":"api_key","key":"b"},"anthropic":{"type":"oauth","access":"a-m","refresh":"r","expires":2}}`
		kept, result := syncConflict(t, t.TempDir(), "pi/auth.json", `{}`, cloud, mac)
		if string(kept) != cloud || len(result.NativeDeferred) != 1 {
			t.Fatalf("kept %s deferred %v", kept, result.NativeDeferred)
		}
	})
}

// The account check is a read-only GET with the agent's own headers; only a
// 200 confirms a token, and redirects are not followed.
func TestNativeCredentialCheckOnline(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]http.Header{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Clone()
		mu.Unlock()
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer good":
			w.Write([]byte(`{}`))
		case "Bearer moved":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()
	previous := nativeCredentialEndpoints
	nativeCredentialEndpoints = map[string]string{"claude": server.URL + "/api/oauth/profile", "chatgpt": server.URL + "/backend-api/wham/usage"}
	defer func() { nativeCredentialEndpoints = previous }()
	ctx := context.Background()
	if !checkNativeCredentialOnline(ctx, "claude", "good", "") || !checkNativeCredentialOnline(ctx, "chatgpt", "good", "acct") {
		t.Fatal("accepted tokens not confirmed")
	}
	if checkNativeCredentialOnline(ctx, "claude", "revoked", "") || checkNativeCredentialOnline(ctx, "claude", "moved", "") || checkNativeCredentialOnline(ctx, "groq", "good", "") {
		t.Fatal("unconfirmed token reported usable")
	}
	mu.Lock()
	defer mu.Unlock()
	if seen["/api/oauth/profile"].Get("anthropic-beta") != "oauth-2025-04-20" || seen["/backend-api/wham/usage"].Get("ChatGPT-Account-Id") != "acct" {
		t.Fatalf("headers: %v", seen)
	}
}

func TestJWTExpiry(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1800000000}`))
	if got := jwtExpiry("h." + payload + ".s"); got != 1_800_000_000_000 {
		t.Fatalf("exp %d", got)
	}
	for _, token := range []string{"", "opaque", "a.b", "h.!!.s", "h." + base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + ".s"} {
		if jwtExpiry(token) != 0 {
			t.Fatalf("%q has no expiry", token)
		}
	}
}

// Claude Code writes its own runtime state into the cloud .claude.json while
// the Mac's account identity also changes; the merge keeps both.
func TestClaudeProfileChangedOnBothComputersMerges(t *testing.T) {
	out, ok := mergeNativeSettings("claude/.claude.json",
		[]byte(`{"oauthAccount":{"emailAddress":"old"},"hasCompletedOnboarding":true,"numStartups":42,"projects":{"/w":{}}}`),
		[]byte(`{"oauthAccount":{"emailAddress":"new"},"hasCompletedOnboarding":true}`))
	var got map[string]any
	json.Unmarshal(out, &got)
	account, _ := got["oauthAccount"].(map[string]any)
	if !ok || account["emailAddress"] != "new" || got["numStartups"] != float64(42) || got["projects"] == nil {
		t.Fatalf("profile merge: %s %v", out, ok)
	}
}
