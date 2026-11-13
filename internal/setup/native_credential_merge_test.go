package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func claudeCredential(token string, expires int64) string {
	b, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "a-" + token, "refreshToken": "r-" + token, "expiresAt": expires}})
	return string(b)
}

// Both computers refreshing the Claude OAuth token must not block
// preparation: the copy with the later expiry is kept, in either direction.
func TestNativeCredentialRefreshedOnBothComputersKeepsNewest(t *testing.T) {
	encode := func(v string) string { return base64.StdEncoding.EncodeToString([]byte(v)) }
	for _, tc := range []struct {
		name       string
		cloud, mac int64
		want       string
	}{
		{"cloud newer", 3000, 2000, "cloud"},
		{"mac newer", 2000, 3000, "mac"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := t.TempDir()
			req := ModelAccountsRequest{Version: 1, Native: true, Files: map[string]string{"claude/.credentials.json": encode(claudeCredential("first", 1000))}}
			if _, err := ApplyNativeAccounts(context.Background(), remote, req); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(remote, ".local/fennec/native/claude/.credentials.json")
			if err := os.WriteFile(target, []byte(claudeCredential("cloud", tc.cloud)), 0o600); err != nil {
				t.Fatal(err)
			}
			req.Files["claude/.credentials.json"] = encode(claudeCredential("mac", tc.mac))
			if _, err := ApplyNativeAccounts(context.Background(), remote, req); err != nil {
				t.Fatalf("credential refresh conflict blocked preparation: %v", err)
			}
			raw, _ := os.ReadFile(target)
			var got map[string]map[string]any
			json.Unmarshal(raw, &got)
			if got["claudeAiOauth"]["refreshToken"] != "r-"+tc.want {
				t.Fatalf("kept the wrong copy, want %s", tc.want)
			}
			if _, err := ApplyNativeAccounts(context.Background(), remote, req); err != nil {
				t.Fatalf("second sync failed: %v", err)
			}
		})
	}
}

func TestNewerNativeCredential(t *testing.T) {
	if _, ok := newerNativeCredential("claude/.credentials.json", []byte(claudeCredential("c", 5)), []byte(claudeCredential("m", 5))); ok {
		t.Fatal("equal expiry must stay a conflict")
	}
	if _, ok := newerNativeCredential("claude/.credentials.json", []byte(`{}`), []byte(claudeCredential("m", 5))); ok {
		t.Fatal("unknown freshness must stay a conflict")
	}
	out, ok := newerNativeCredential("codex/auth.json", []byte(`{"tokens":{"refresh_token":"c"},"last_refresh":"2026-11-12T10:00:00Z"}`), []byte(`{"tokens":{"refresh_token":"m"},"last_refresh":"2026-11-13T10:00:00Z"}`))
	if !ok || string(out) != `{"tokens":{"refresh_token":"m"},"last_refresh":"2026-11-13T10:00:00Z"}` {
		t.Fatalf("codex: %s %v", out, ok)
	}
	out, ok = newerNativeCredential("pi/auth.json",
		[]byte(`{"anthropic":{"type":"oauth","refresh":"c","expires":9},"openai":{"type":"oauth","refresh":"c","expires":1},"groq":{"type":"api_key","key":"k"}}`),
		[]byte(`{"anthropic":{"type":"oauth","refresh":"m","expires":2},"openai":{"type":"oauth","refresh":"m","expires":8},"groq":{"type":"api_key","key":"k"}}`))
	var pi map[string]map[string]any
	json.Unmarshal(out, &pi)
	if !ok || pi["anthropic"]["refresh"] != "c" || pi["openai"]["refresh"] != "m" || pi["groq"]["key"] != "k" {
		t.Fatalf("pi per-provider: %s %v", out, ok)
	}
	if _, ok := newerNativeCredential("pi/auth.json", []byte(`{"groq":{"type":"api_key","key":"a"}}`), []byte(`{"groq":{"type":"api_key","key":"b"}}`)); ok {
		t.Fatal("changed API keys on both sides must stay a conflict")
	}
}
