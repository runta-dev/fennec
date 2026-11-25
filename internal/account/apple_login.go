package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// LoginApple starts a Sign in with Apple attempt. The iPhone's own Apple
// sheet asks the person, not a browser; CompleteApple then takes the identity
// token that the sheet returns.
func (m *Manager) LoginApple(config NativeOAuthConfig) (Snapshot, error) {
	return m.loginNative("apple", config, "")
}

// CompleteApple accepts the Apple identity token for the current attempt.
// nonce is the raw value whose SHA-256 the app put in the Apple request.
// Supabase verifies the token (Apple's signature, the audience, the nonce)
// and finds or creates the user: the same email links to the same user, as
// for Google and GitHub. A hidden address arrives as a private relay address.
// fullName is what Apple shares on the first authorization only, or "".
func (m *Manager) CompleteApple(idToken, nonce, fullName string, epoch uint64) bool {
	if idToken == "" || len(idToken) > 16384 || nonce == "" || len(nonce) > 256 {
		return false
	}
	m.mu.Lock()
	a := m.native
	if a == nil || a.provider != "apple" || m.state.Status != "authorizing" || m.state.Epoch != epoch || a.callbackUsed || time.Now().After(a.expires) {
		m.mu.Unlock()
		return false
	}
	a.callbackUsed = true
	parent := m.session
	m.mu.Unlock()
	go m.exchangeApple(parent, epoch, a, idToken, nonce, strings.TrimSpace(fullName))
	return true
}

func (m *Manager) exchangeApple(parent context.Context, epoch uint64, a *nativeAttempt, idToken, nonce, fullName string) {
	ctx, cancel := context.WithDeadline(parent, a.expires)
	defer cancel()
	// registerNative sends the attempt ID that startNative records.
	select {
	case <-a.started:
	case <-ctx.Done():
		return
	}
	var session struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	err := m.loginRequest(ctx, "POST", a.config.AuthURL+"/auth/v1/token?grant_type=id_token", "", a.config.PublishableKey, map[string]string{"provider": "apple", "id_token": idToken, "nonce": nonce}, &session)
	if err != nil || session.AccessToken == "" {
		detail := "auth refused the Apple identity token"
		var rejected loginRejected
		if errors.As(err, &rejected) {
			// 400 or 422 when Supabase has no Apple provider for this app's bundle ID.
			detail = fmt.Sprintf("%s (HTTP %d)", detail, rejected.status)
		}
		m.failDetail(epoch, "authorization_failed", detail)
		return
	}
	token := session.AccessToken
	if fullName != "" && session.RefreshToken != "" {
		token = m.saveAppleName(ctx, a, token, session.RefreshToken, fullName)
	}
	m.registerNative(ctx, epoch, a, token)
}

// saveAppleName keeps the name Apple shared as Supabase user metadata, then
// refreshes the session so that the token Runta reads carries it. The
// identity token itself never contains the name. On any failure, the account
// is created without a name, as for an email sign-in.
func (m *Manager) saveAppleName(ctx context.Context, a *nativeAttempt, token, refresh, name string) string {
	if len(name) > 200 {
		return token
	}
	if m.loginRequest(ctx, "PUT", a.config.AuthURL+"/auth/v1/user", token, a.config.PublishableKey, map[string]any{"data": map[string]string{"full_name": name}}, nil) != nil {
		return token
	}
	var session struct {
		AccessToken string `json:"access_token"`
	}
	if m.loginRequest(ctx, "POST", a.config.AuthURL+"/auth/v1/token?grant_type=refresh_token", "", a.config.PublishableKey, map[string]string{"refresh_token": refresh}, &session) != nil || session.AccessToken == "" {
		return token
	}
	return session.AccessToken
}
