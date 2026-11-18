package account

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

const NativeCallbackURL = "fennec://auth/callback"

// NativeOAuthConfig contains public deployment configuration, never a service key.
type NativeOAuthConfig struct{ AuthURL, PublishableKey, BackendURL, DashboardURL string }

type LoginOrganization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type nativeAttempt struct {
	config              NativeOAuthConfig
	verifier, attemptID string
	expires             time.Time
	callbackUsed        bool
	email               string
	turnstileToken      string
}

// DeepLinkRoute recognizes complete literal routes, never a scheme alone.
func DeepLinkRoute(raw string) string {
	if len(raw) > 8192 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "fennec" || u.User != nil || u.Opaque != "" || u.Port() != "" {
		return ""
	}
	if u.Host == "auth" && u.EscapedPath() == "/callback" && !strings.Contains(raw, "#") {
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return ""
		}
		for k, v := range q {
			if len(v) != 1 {
				return ""
			}
			switch k {
			case "code", "error", "error_code", "error_description":
			default:
				return ""
			}
		}
		if (q.Get("code") == "") == (q.Get("error") == "") {
			return ""
		}
		if q.Get("code") != "" && (q.Has("error_code") || q.Has("error_description")) {
			return ""
		}
		return "auth/callback"
	}
	if u.Host == "pair" && u.Path == "" && u.RawQuery == "" && !u.ForceQuery {
		_, fragment, _ := strings.Cut(raw, "#")
		q, err := url.ParseQuery(fragment)
		if err == nil && len(q) == 2 && len(q["code"]) == 1 && len(q["api"]) == 1 && q.Get("code") != "" && q.Get("api") != "" {
			return "pair"
		}
	}
	return ""
}

// LoginNative starts a client-owned PKCE flow. Supabase credentials remain in
// memory while the Dashboard API issues a separate Runta API key.
func (m *Manager) LoginNative(provider string, config NativeOAuthConfig) (Snapshot, error) {
	if provider != "google" && provider != "github" {
		return m.Snapshot(), errors.New("unsupported provider")
	}
	return m.loginNative(provider, config, "")
}

// LoginEmail asks Runta to send a sign-in code without opening a hosted page.
// Runta's /v1/auth/login, not Supabase's public /otp, sends it: Runta's
// Turnstile check and rate limit apply, and Resend delivers Runta's message
// with the code. Supabase's own mailer sends a link without a code.
func (m *Manager) LoginEmail(email, turnstileToken string, config NativeOAuthConfig) (Snapshot, error) {
	email = strings.TrimSpace(email)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return m.Snapshot(), errors.New("invalid email login request")
	}
	turnstileToken = strings.TrimSpace(turnstileToken)
	if turnstileToken == "" || len(turnstileToken) > 2048 {
		return m.Snapshot(), errors.New("verification challenge required")
	}
	if u, e := url.Parse(config.DashboardURL); e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return m.Snapshot(), errors.New("invalid auth configuration")
	}
	return m.loginNativeWith("email", config, email, turnstileToken)
}

func (m *Manager) loginNative(provider string, config NativeOAuthConfig, email string) (Snapshot, error) {
	return m.loginNativeWith(provider, config, email, "")
}

func (m *Manager) loginNativeWith(provider string, config NativeOAuthConfig, email, turnstileToken string) (Snapshot, error) {
	for _, origin := range []string{config.AuthURL, config.BackendURL} {
		u, e := url.Parse(origin)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return m.Snapshot(), errors.New("invalid auth configuration")
		}
	}
	if !strings.HasPrefix(config.PublishableKey, "sb_publishable_") {
		return m.Snapshot(), errors.New("invalid publishable key")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Status == "authorizing" || m.state.Status == "authorized" {
		return m.state, nil
	}
	ctx, epoch := m.resetLocked("authorizing")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	oldCancel := m.cancel
	m.cancel = func() { cancel(); oldCancel() }
	a := &nativeAttempt{config: config, expires: time.Now().Add(10 * time.Minute), email: email, turnstileToken: turnstileToken}
	verifier := make([]byte, 32)
	if _, e := rand.Read(verifier); e != nil {
		cancel()
		m.state.Status = "signed_out"
		m.notifyLocked()
		return m.state, e
	}
	a.verifier = base64.RawURLEncoding.EncodeToString(verifier)
	m.native = a
	go m.startNative(ctx, epoch, a, provider)
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.state.Epoch == epoch && m.state.Status == "authorizing" {
			m.native = nil
			m.state = Snapshot{Epoch: epoch, Status: "signed_out", Error: "authorization_expired"}
			m.notifyLocked()
		}
	}()
	return m.state, nil
}

func (m *Manager) nativeRequest(ctx context.Context, a *nativeAttempt, method, path, token string, body, out any) error {
	return m.loginRequest(ctx, method, a.config.BackendURL+path, token, "", body, out)
}
func (m *Manager) loginRequest(ctx context.Context, method, endpoint, token, key string, body, out any) error {
	var payload io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		payload = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if e != nil {
		return errors.New("invalid login request")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("apikey", key)
	}
	resp, e := m.http.Do(req)
	if e != nil {
		return errors.New("login request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("login request rejected")
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return e
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out) != nil {
		return errors.New("invalid login response")
	}
	return nil
}
func (m *Manager) startNative(ctx context.Context, epoch uint64, a *nativeAttempt, provider string) {
	var start struct {
		Data struct {
			AttemptID string `json:"attempt_id"`
		} `json:"data"`
	}
	var err error
	if provider == "email" {
		var redirect string
		redirect, err = nativeEmailRedirect(a.config.DashboardURL)
		if err == nil {
			err = m.nativeRequest(ctx, a, "POST", "/v1/auth/login", "", map[string]string{"email": a.email, "turnstile_token": a.turnstileToken, "redirect_to": redirect}, &start)
		}
		a.turnstileToken = ""
	} else {
		err = m.nativeRequest(ctx, a, "POST", "/v1/auth/oauth/start", "", map[string]string{"provider": provider}, &start)
	}
	if err != nil || start.Data.AttemptID == "" {
		m.fail(epoch, "authorization_failed")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Epoch != epoch || m.native != a || ctx.Err() != nil {
		return
	}
	a.attemptID = start.Data.AttemptID
	if provider == "email" {
		m.state.EmailCode = true
		m.state.ExpiresAt = a.expires
		m.notifyLocked()
		return
	}
	challenge := sha256.Sum256([]byte(a.verifier))
	q := url.Values{"provider": {provider}, "redirect_to": {NativeCallbackURL}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"s256"}}
	if provider == "github" {
		q.Set("scopes", "read:user user:email")
	}
	m.state.URL = a.config.AuthURL + "/auth/v1/authorize?" + q.Encode()
	m.state.ExpiresAt = a.expires
	m.notifyLocked()
}

// CompleteNative accepts only the current browser generation. Its code is bound
// to the locally retained verifier; URLs never select an environment or endpoint.
func (m *Manager) CompleteNative(raw string, epoch uint64) bool {
	if DeepLinkRoute(raw) != "auth/callback" {
		return false
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	m.mu.Lock()
	a := m.native
	if a == nil || a.email != "" || m.state.Status != "authorizing" || m.state.Epoch != epoch || a.callbackUsed || time.Now().After(a.expires) {
		m.mu.Unlock()
		return false
	}
	a.callbackUsed = true
	m.state.URL = ""
	m.notifyLocked()
	ctx := m.session
	m.mu.Unlock()
	if q.Get("error") != "" {
		m.fail(epoch, "access_denied")
		return true
	}
	go m.exchangeNative(ctx, epoch, a, q.Get("code"))
	return true
}
func (m *Manager) exchangeNative(parent context.Context, epoch uint64, a *nativeAttempt, code string) {
	ctx, cancel := context.WithDeadline(parent, a.expires)
	defer cancel()
	var session struct {
		AccessToken string `json:"access_token"`
	}
	if e := m.loginRequest(ctx, "POST", a.config.AuthURL+"/auth/v1/token?grant_type=pkce", "", a.config.PublishableKey, map[string]string{"auth_code": code, "code_verifier": a.verifier}, &session); e != nil || session.AccessToken == "" {
		m.fail(epoch, "authorization_failed")
		return
	}
	m.registerNative(ctx, epoch, a, session.AccessToken)
}

func (m *Manager) registerNative(ctx context.Context, epoch uint64, a *nativeAttempt, token string) {
	body := map[string]string{}
	if a.attemptID != "" {
		body["attempt_id"] = a.attemptID
	}
	if e := m.nativeRequest(ctx, a, "POST", "/v1/auth/session", token, body, nil); e != nil {
		m.fail(epoch, "authorization_failed")
		return
	}
	var organizations struct {
		Data []LoginOrganization `json:"data"`
	}
	if err := m.nativeRequest(ctx, a, "GET", "/v1/organizations", token, nil, &organizations); err != nil || len(organizations.Data) == 0 || organizations.Data[0].ID == "" {
		m.fail(epoch, "organization_unavailable")
		return
	}
	// Match Dashboard's default organization; never ask for a separate device grant.
	path := "/v1/organizations/" + url.PathEscape(organizations.Data[0].ID) + "/api-keys"
	var created struct {
		Data struct {
			Key    string `json:"plaintext_key"`
			APIKey struct {
				ID string `json:"id"`
			} `json:"api_key"`
		} `json:"data"`
	}
	request := map[string]any{"name": "Fennec iPhone", "expires_at": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339), "scopes": []string{"identity:self:read", "org:read", "billing:read", "runtime:read", "runtime:manage", "events:read", "secrets:read", "secrets:manage", "ai-agent-auth:read", "github:read", "github:manage"}}
	if err := m.nativeRequest(ctx, a, "POST", path, token, request, &created); err != nil || created.Data.Key == "" {
		m.fail(epoch, "credential_creation_failed")
		return
	}
	saved := false
	defer func() {
		if !saved && created.Data.APIKey.ID != "" {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = m.nativeRequest(cleanup, a, "DELETE", path+"/"+url.PathEscape(created.Data.APIKey.ID), token, nil, nil)
		}
	}()
	m.credentialsMu.Lock()
	defer m.credentialsMu.Unlock()
	m.mu.Lock()
	valid := m.state.Epoch == epoch && m.native == a && ctx.Err() == nil
	m.mu.Unlock()
	if !valid {
		return
	}
	if err := m.credentials.Write(ctx, created.Data.Key); err != nil {
		m.fail(epoch, "credential_save_failed")
		return
	}
	m.mu.Lock()
	m.token = created.Data.Key
	m.native = nil
	m.state = Snapshot{Epoch: epoch, Status: "authorized"}
	m.notifyLocked()
	m.mu.Unlock()
	saved = true
}

// VerifyEmail accepts only the active email attempt and never publishes its token.
func (m *Manager) VerifyEmail(code string, epoch uint64) bool {
	code = strings.TrimSpace(code)
	if len(code) < 6 || len(code) > 10 || strings.IndexFunc(code, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return false
	}
	m.mu.Lock()
	a := m.native
	if a == nil || a.email == "" || m.state.Epoch != epoch || m.state.Status != "authorizing" || !m.state.EmailCode || a.callbackUsed || time.Now().After(a.expires) {
		m.mu.Unlock()
		return false
	}
	a.callbackUsed = true
	m.state.Error = ""
	m.state.EmailVerifying = true
	m.notifyLocked()
	parent := m.session
	m.mu.Unlock()
	go func() {
		ctx, cancel := context.WithDeadline(parent, a.expires)
		defer cancel()
		var session struct {
			AccessToken string `json:"access_token"`
		}
		err := m.loginRequest(ctx, "POST", a.config.AuthURL+"/auth/v1/verify", "", a.config.PublishableKey, map[string]string{"email": a.email, "token": code, "type": "email"}, &session)
		if err != nil || session.AccessToken == "" {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.native == a && m.state.Epoch == epoch && ctx.Err() == nil {
				a.callbackUsed = false
				m.state.EmailVerifying = false
				m.state.Error = "invalid_email_code"
				m.notifyLocked()
			}
			return
		}
		m.registerNative(ctx, epoch, a, session.AccessToken)
	}()
	return true
}

// nativeEmailRedirect is the Dashboard callback Runta requires for the link it
// sends beside the code. Its browser binding is the digest of a secret that is
// discarded here, so the link can't be redeemed in any browser and only the
// code signs this app in.
func nativeEmailRedirect(dashboard string) (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	digest := sha256.Sum256(secret)
	return dashboard + "/auth/callback?login_binding=" + base64.RawURLEncoding.EncodeToString(digest[:]), nil
}
