package setup

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func accountFiles(values map[string]string) map[string]string {
	files := map[string]string{}
	for name, value := range values {
		files[name] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	return files
}
func accountJWT(value string) string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(value)) + ".signature"
}
func TestNativeAccountViewShowsOnlyExplicitSubscriptionMetadata(t *testing.T) {
	token := accountJWT(`{"email":"me@example.com","https://api.openai.com/auth":{"chatgpt_plan_type":"pro"}}`)
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"access_token": "private-access-token", "refresh_token": "private-refresh-token", "id_token": token}})
	view := NativeAccountFromFiles("codex", accountFiles(map[string]string{"codex/auth.json": string(auth)}))
	if view.Status != "signed_in" || len(view.Accounts) != 1 || view.Accounts[0].Email != "me@example.com" || view.Accounts[0].Subscription != "ChatGPT Pro" {
		t.Fatal(view)
	}
	raw, _ := json.Marshal(view)
	for _, secret := range []string{"private-access-token", "private-refresh-token", token, "quota", "remaining"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("credential or fabricated usage leaked into account view")
		}
	}
	view = NativeAccountFromFiles("codex", accountFiles(map[string]string{"codex/auth.json": `{"tokens":{"access_token":"opaque"}}`}))
	if view.Accounts[0].Subscription != "ChatGPT" {
		t.Fatal("unknown plan guessed")
	}
}
func TestNativeAPIIdentityDoesNotClaimStaleSubscription(t *testing.T) {
	token := accountJWT(`{"email":"old@example.com","https://api.openai.com/auth":{"chatgpt_plan_type":"pro"}}`)
	auth, _ := json.Marshal(map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "private-api-key", "tokens": map[string]string{"access_token": "old-access", "id_token": token}})
	view := NativeAccountFromFiles("codex", accountFiles(map[string]string{"codex/auth.json": string(auth)}))
	if len(view.Accounts) != 1 || view.Accounts[0].AuthType != "api_key" || view.Accounts[0].Email != "" || view.Accounts[0].Subscription != "" {
		t.Fatal("stale OAuth metadata used for API identity", view)
	}
}
func TestNativeAccountAbsentIsQuietlySignedOut(t *testing.T) {
	for _, agent := range []string{"codex", "claude", "pi"} {
		view := NativeAccountFromFiles(agent, nil)
		if view.Status != "signed_out" || len(view.Accounts) != 0 {
			t.Fatal(view)
		}
	}
}
func TestClaudeSubscriptionAndPiProviders(t *testing.T) {
	view := NativeAccountFromFiles("claude", accountFiles(map[string]string{"claude/.credentials.json": `{"claudeAiOauth":{"accessToken":"private-token","subscriptionType":"max"}}`, "claude/.claude.json": `{"oauthAccount":{"emailAddress":"me@example.com"}}`}))
	if len(view.Accounts) != 1 || view.Accounts[0].Subscription != "Claude Max" || view.Accounts[0].Email != "me@example.com" {
		t.Fatal(view)
	}
	view = NativeAccountFromFiles("pi", accountFiles(map[string]string{"pi/auth.json": `{"anthropic":{"type":"oauth","access":"private-token"},"magpie":{"key":"gateway"},"openai":{"key":"api-key"}}`}))
	if len(view.Accounts) != 2 || view.Accounts[0].Provider != "anthropic" || view.Accounts[1].AuthType != "api_key" {
		t.Fatal(view)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "private-token") || strings.Contains(string(raw), "gateway") {
		t.Fatal("secret or gateway identity exposed as native")
	}
}
func TestLocalGatewayPlaceholderIsNotNativeSignIn(t *testing.T) {
	view := NativeAccountFromFiles("claude", accountFiles(map[string]string{"claude/settings.json": `{"env":{"ANTHROPIC_BASE_URL":"http://localhost:3425"}}`, "env.json": `{"ANTHROPIC_AUTH_TOKEN":"placeholder"}`}))
	if view.Status != "signed_out" {
		t.Fatal("gateway placeholder reported as native login")
	}
}

func TestClaudeExplicitTokenDoesNotReuseAnotherAccountsMetadata(t *testing.T) {
	view := NativeAccountFromFiles("claude", accountFiles(map[string]string{"claude/.credentials.json": `{"claudeAiOauth":{"accessToken":"old-token","subscriptionType":"max"}}`, "claude/.claude.json": `{"oauthAccount":{"emailAddress":"old@example.com"}}`, "env.json": `{"CLAUDE_CODE_OAUTH_TOKEN":"different-account"}`}))
	if view.Status != "signed_in" || view.Accounts[0].Email != "" || view.Accounts[0].Subscription != "Claude" {
		t.Fatal("unrelated saved account metadata attributed to explicit token", view)
	}
}
