package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A credential file that changed on both computers since the last sync is
// resolved without asking anyone and without ever failing preparation:
//
//   - A copy without any credential (the agent signed out, or cleared its
//     tokens after another computer rotated the shared refresh token) never
//     wins; the other copy replaces it, since nothing usable is lost.
//   - Copies sharing one refresh token belong to the same sign-in; the one
//     whose access token expires later is kept.
//   - Otherwise each copy is confirmed usable when its access token has not
//     expired and the provider's read-only account endpoint accepts it. Both
//     usable: the cloud copy stays unless the Mac copy is strictly newer.
//     Exactly one usable: that copy is kept.
//   - Neither confirmed (expired, offline, unknown provider, API keys): both
//     computers keep their own copy and the next sync decides again.
//
// Never a token refresh (it rotates the grant) and never a model call.

// nativeCredentialCheck reports whether a provider accepts an OAuth access
// token now, using a read-only, non-inference account endpoint. Tests replace
// it. Only an explicit success counts; errors and other statuses do not.
var nativeCredentialCheck = checkNativeCredentialOnline

// nativeCredentialNow is the clock for access-token expiry.
var nativeCredentialNow = time.Now

const nativeCredentialCheckTimeout = 5 * time.Second

// nativeCredentialEndpoints are the read-only account endpoints the agents
// themselves call: Claude Code's OAuth profile and Codex's usage summary.
var nativeCredentialEndpoints = map[string]string{
	"claude":  "https://api.anthropic.com/api/oauth/profile",
	"chatgpt": "https://chatgpt.com/backend-api/wham/usage",
}

type nativeOAuthEntry struct {
	access, refresh, account, key string
	provider                      string // "claude", "chatgpt", or "" when no account endpoint is known
	expires                       int64  // access-token expiry in Unix milliseconds; 0 when unknown
	fresh                         int64  // ordering of refreshes; the expiry when known
}

// signedOut reports that the copy holds no credential at all.
func (e nativeOAuthEntry) signedOut() bool {
	return e.access == "" && e.refresh == "" && e.key == ""
}

type nativeCredentialChoice int

const (
	keepCloudCredential nativeCredentialChoice = iota
	keepMacCredential
	deferCredential
)

// resolveNativeCredential returns the copy to keep, or false when neither copy
// can be confirmed and both computers keep their own.
func resolveNativeCredential(ctx context.Context, name string, cloud, local []byte) ([]byte, bool) {
	var c, l map[string]any
	if json.Unmarshal(cloud, &c) != nil || json.Unmarshal(local, &l) != nil {
		return nil, false
	}
	if c == nil {
		c = map[string]any{}
	}
	if l == nil {
		l = map[string]any{}
	}
	pick := func(choice nativeCredentialChoice) ([]byte, bool) {
		switch choice {
		case keepCloudCredential:
			return cloud, true
		case keepMacCredential:
			return local, true
		}
		return nil, false
	}
	switch name {
	case cursorAuthFile:
		return pick(chooseNativeCredential(ctx, cursorEntry(c), cursorEntry(l)))
	case grokAuthFile:
		merged := map[string]any{}
		for scope, value := range l {
			merged[scope] = value
		}
		for scope, cv := range c {
			lv, exists := l[scope]
			if !exists || jsonEqual(cv, lv) {
				merged[scope] = cv
				continue
			}
			cm, _ := cv.(map[string]any)
			lm, _ := lv.(map[string]any)
			switch chooseNativeCredential(ctx, grokEntry(cm), grokEntry(lm)) {
			case keepCloudCredential:
				merged[scope] = cv
			case keepMacCredential:
				merged[scope] = lv
			default:
				return nil, false
			}
		}
		out, err := json.Marshal(merged)
		return out, err == nil
	case "claude/.credentials.json":
		return pick(chooseNativeCredential(ctx, claudeEntry(c), claudeEntry(l)))
	case "codex/auth.json":
		return pick(chooseNativeCredential(ctx, codexEntry(c), codexEntry(l)))
	case kimiCredentialFile:
		return pick(chooseNativeCredential(ctx, kimiEntry(c), kimiEntry(l)))
	case "pi/auth.json", "opencode/data/opencode/auth.json":
		merged := map[string]any{}
		for key, value := range l {
			merged[key] = value
		}
		for key, cv := range c {
			lv, exists := l[key]
			if !exists || jsonEqual(cv, lv) {
				merged[key] = cv
				continue
			}
			switch chooseNativeCredential(ctx, providerEntry(key, cv), providerEntry(key, lv)) {
			case keepCloudCredential:
				merged[key] = cv
			case keepMacCredential:
				merged[key] = lv
			default:
				return nil, false
			}
		}
		out, err := json.Marshal(merged)
		return out, err == nil
	}
	return nil, false
}

func chooseNativeCredential(ctx context.Context, cloud, mac nativeOAuthEntry) nativeCredentialChoice {
	switch {
	case cloud.signedOut() && mac.signedOut():
		return keepCloudCredential
	case cloud.signedOut():
		return keepMacCredential
	case mac.signedOut():
		return keepCloudCredential
	case cloud.refresh != "" && cloud.refresh == mac.refresh:
		if mac.fresh > cloud.fresh {
			return keepMacCredential
		}
		return keepCloudCredential
	}
	var cloudValid, macValid bool
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); cloudValid = nativeCredentialUsable(ctx, cloud) }()
	go func() { defer wait.Done(); macValid = nativeCredentialUsable(ctx, mac) }()
	wait.Wait()
	switch {
	case cloudValid && macValid:
		if mac.fresh > cloud.fresh {
			return keepMacCredential
		}
		return keepCloudCredential
	case cloudValid:
		return keepCloudCredential
	case macValid:
		return keepMacCredential
	}
	return deferCredential
}

func nativeCredentialUsable(ctx context.Context, e nativeOAuthEntry) bool {
	if e.access == "" || e.provider == "" {
		return false
	}
	if e.expires > 0 && e.expires <= nativeCredentialNow().Add(time.Minute).UnixMilli() {
		return false // only a refresh could confirm it, and a refresh rotates the grant
	}
	check, cancel := context.WithTimeout(ctx, nativeCredentialCheckTimeout)
	defer cancel()
	return nativeCredentialCheck(check, e.provider, e.access, e.account)
}

func checkNativeCredentialOnline(ctx context.Context, provider, access, account string) bool {
	endpoint := nativeCredentialEndpoints[provider]
	if endpoint == "" {
		return false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	request.Header.Set("Authorization", "Bearer "+access)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "fennec")
	switch provider {
	case "claude":
		request.Header.Set("anthropic-beta", "oauth-2025-04-20")
	case "chatgpt":
		if account != "" {
			request.Header.Set("ChatGPT-Account-Id", account)
		}
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	return response.StatusCode == http.StatusOK
}

func claudeEntry(v map[string]any) nativeOAuthEntry {
	oauth, _ := v["claudeAiOauth"].(map[string]any)
	expires := int64(numberValue(oauth["expiresAt"]))
	return nativeOAuthEntry{access: stringValue(oauth["accessToken"]), refresh: stringValue(oauth["refreshToken"]), provider: "claude", expires: expires, fresh: expires}
}

func codexEntry(v map[string]any) nativeOAuthEntry {
	tokens, _ := v["tokens"].(map[string]any)
	e := nativeOAuthEntry{access: stringValue(tokens["access_token"]), refresh: stringValue(tokens["refresh_token"]), account: stringValue(tokens["account_id"]), key: stringValue(v["OPENAI_API_KEY"]), provider: "chatgpt"}
	if exp := jwtExpiry(e.access); exp > 0 {
		e.expires, e.fresh = exp, exp
	} else if t, err := time.Parse(time.RFC3339Nano, stringValue(v["last_refresh"])); err == nil {
		e.fresh = t.UnixMilli()
	}
	return e
}

// providerEntry reads a Pi or OpenCode provider entry. OAuth entries carry
// access, refresh and expires (Unix milliseconds); others carry an API key.
func providerEntry(provider string, value any) nativeOAuthEntry {
	entry, _ := value.(map[string]any)
	e := nativeOAuthEntry{access: stringValue(entry["access"]), refresh: stringValue(entry["refresh"]), account: stringValue(entry["accountId"]), key: stringValue(entry["key"]) + stringValue(entry["apiKey"])}
	e.expires = int64(numberValue(entry["expires"]))
	e.fresh = e.expires
	if e.access != "" {
		switch provider {
		case "anthropic":
			e.provider = "claude"
		case "openai-codex", "openai":
			e.provider = "chatgpt"
		}
	}
	return e
}

// jwtExpiry reads the exp claim (seconds) of an unverified JWT as Unix
// milliseconds; the token itself is never logged or returned.
func jwtExpiry(token string) int64 {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return 0
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp <= 0 {
		return 0
	}
	return int64(claims.Exp * 1000)
}

func numberValue(v any) float64 {
	n, _ := v.(float64)
	return n
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
