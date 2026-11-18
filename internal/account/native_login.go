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
	"net/url"
	"strings"
	"time"
)

const NativeCallbackURL = "fennec://auth/callback"

// NativeOAuthConfig contains public deployment configuration, never a service key.
type NativeOAuthConfig struct{ AuthURL, PublishableKey, BackendURL string }

type LoginOrganization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type LoginConsent struct {
	ClientName              string              `json:"client_name"`
	Access                  string              `json:"access"`
	CredentialExpiresInDays int                 `json:"credential_expires_in_days"`
	Organizations           []LoginOrganization `json:"organizations"`
}
type nativeAttempt struct {
	config                                           NativeOAuthConfig
	verifier, attemptID, deviceCode, userCode, token string
	expires                                          time.Time
	callbackUsed, approving                          bool
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
// memory until explicit organization consent issues a separate Runta credential.
func (m *Manager) LoginNative(provider string, config NativeOAuthConfig) (Snapshot, error) {
	if provider != "google" && provider != "github" {
		return m.Snapshot(), errors.New("unsupported provider")
	}
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
	a := &nativeAttempt{config: config, expires: time.Now().Add(10 * time.Minute)}
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
	if e := m.nativeRequest(ctx, a, "POST", "/v1/auth/oauth/start", "", map[string]string{"provider": provider}, &start); e != nil || start.Data.AttemptID == "" {
		m.fail(epoch, "authorization_failed")
		return
	}
	var device struct {
		Data struct {
			DeviceCode string    `json:"device_code"`
			UserCode   string    `json:"user_code"`
			ExpiresAt  time.Time `json:"expires_at"`
		} `json:"data"`
	}
	status, e := m.post(ctx, "authorization", map[string]string{"client_id": "runta_crew", "device_name": "Fennec iPhone", "app_url": m.DashboardURL()}, &device)
	if e != nil || status < 200 || status >= 300 || device.Data.DeviceCode == "" || device.Data.UserCode == "" {
		m.fail(epoch, "authorization_failed")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Epoch != epoch || m.native != a || ctx.Err() != nil {
		return
	}
	a.attemptID = start.Data.AttemptID
	a.deviceCode = device.Data.DeviceCode
	a.userCode = device.Data.UserCode
	if !device.Data.ExpiresAt.IsZero() && device.Data.ExpiresAt.Before(a.expires) {
		a.expires = device.Data.ExpiresAt
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
	if a == nil || m.state.Status != "authorizing" || m.state.Epoch != epoch || a.callbackUsed || time.Now().After(a.expires) || a.deviceCode == "" {
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
	if e := m.nativeRequest(ctx, a, "POST", "/v1/auth/session", session.AccessToken, map[string]string{"attempt_id": a.attemptID}, nil); e != nil {
		m.fail(epoch, "authorization_failed")
		return
	}
	var consent struct {
		Data LoginConsent `json:"data"`
	}
	if e := m.nativeRequest(ctx, a, "GET", "/v1/auth/device/authorization?code="+url.QueryEscape(a.userCode), session.AccessToken, nil, &consent); e != nil || len(consent.Data.Organizations) == 0 {
		m.fail(epoch, "authorization_failed")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Epoch != epoch || m.native != a || ctx.Err() != nil {
		return
	}
	a.token = session.AccessToken
	a.verifier = ""
	m.state.Consent = &consent.Data
	m.notifyLocked()
}

// ApproveNative requires explicit selection from the server's organization list.
func (m *Manager) ApproveNative(organization string, epoch uint64) bool {
	m.mu.Lock()
	a := m.native
	if a == nil || m.state.Epoch != epoch || m.state.Status != "authorizing" || m.state.Consent == nil || a.approving || time.Now().After(a.expires) {
		m.mu.Unlock()
		return false
	}
	valid := false
	for _, o := range m.state.Consent.Organizations {
		if o.ID == organization {
			valid = true
		}
	}
	if !valid {
		m.mu.Unlock()
		return false
	}
	a.approving = true
	m.state.Consent = nil
	m.notifyLocked()
	ctx := m.session
	m.mu.Unlock()
	go func() {
		ctx, cancel := context.WithDeadline(ctx, a.expires)
		defer cancel()
		if e := m.nativeRequest(ctx, a, "POST", "/v1/auth/device/authorization/approve", a.token, map[string]string{"code": a.userCode, "organization_id": organization}, nil); e != nil {
			m.fail(epoch, "authorization_failed")
			return
		}
		m.pollTokenFrom(ctx, epoch, a.deviceCode, 0, 5*time.Second)
		m.mu.Lock()
		if m.native == a {
			m.native = nil
		}
		m.mu.Unlock()
	}()
	return true
}
