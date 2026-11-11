// Package account owns the device-login lifecycle. Tokens never appear in snapshots.
package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type Credentials interface {
	Read(context.Context) (string, error)
	Write(context.Context, string) error
	Delete(context.Context) error
}

var ErrNotFound = errors.New("credential not found")

type Snapshot struct {
	Revision  uint64    `json:"revision"`
	Epoch     uint64    `json:"epoch"`
	Status    string    `json:"status"`
	UserCode  string    `json:"user_code,omitempty"`
	URL       string    `json:"verification_url,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type Manager struct {
	mu            sync.Mutex
	credentialsMu sync.Mutex
	// Credential operations are serialized with epoch changes to prevent stale
	// authorization responses from restoring credentials after logout.
	credentials Credentials
	endpoint    string
	http        *http.Client
	parent      context.Context
	session     context.Context
	cancel      context.CancelFunc
	state       Snapshot
	token       string
	changed     chan struct{}
	revision    uint64
}

func New(ctx context.Context, credentials Credentials, endpoint string) *Manager {
	return &Manager{parent: ctx, credentials: credentials, endpoint: endpoint, http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, state: Snapshot{Status: "signed_out"}, changed: make(chan struct{})}
}
func (m *Manager) notifyLocked() {
	m.revision++
	m.state.Revision = m.revision
	close(m.changed)
	m.changed = make(chan struct{})
}
func (m *Manager) Snapshot() Snapshot { m.mu.Lock(); defer m.mu.Unlock(); return m.state }
func (m *Manager) Token() (string, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token, m.state.Epoch
}
func (m *Manager) Changes() <-chan struct{} { m.mu.Lock(); defer m.mu.Unlock(); return m.changed }
func (m *Manager) resetLocked(status string) (context.Context, uint64) {
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(m.parent)
	m.session = ctx
	m.cancel = cancel
	m.state = Snapshot{Epoch: m.state.Epoch + 1, Status: status}
	m.token = ""
	m.notifyLocked()
	return ctx, m.state.Epoch
}

// Session returns an account-scoped context, invalidated by logout or replacement.
// Only daemon code may use the token; it must never enter a public response.
func (m *Manager) Session() (context.Context, string, uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.session, m.token, m.state.Epoch, m.state.Status == "authorized" && m.token != ""
}
func (m *Manager) Restore(ctx context.Context) error {
	m.credentialsMu.Lock()
	defer m.credentialsMu.Unlock()
	m.mu.Lock()
	if m.state.Status != "signed_out" {
		m.mu.Unlock()
		return nil
	}
	_, epoch := m.resetLocked("restoring")
	m.mu.Unlock()
	token, err := m.credentials.Read(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Epoch != epoch {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		m.state.Status = "signed_out"
		m.notifyLocked()
		return nil
	}
	if err != nil {
		m.state.Status = "signed_out"
		m.state.Error = "credentials_unavailable"
		m.notifyLocked()
		return err
	}
	if token == "" {
		m.state.Status = "signed_out"
		m.notifyLocked()
		return ErrNotFound
	}
	m.state.Status = "authorized"
	m.token = token
	m.notifyLocked()
	return nil
}
func (m *Manager) Logout(ctx context.Context) error {
	m.credentialsMu.Lock()
	defer m.credentialsMu.Unlock()
	if err := m.credentials.Delete(ctx); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetLocked("signed_out")
	return nil
}
func (m *Manager) Login() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Status == "authorizing" || m.state.Status == "authorized" {
		return m.state
	}
	ctx, epoch := m.resetLocked("authorizing")
	go m.authorize(ctx, epoch)
	return m.state
}

// Reauthenticate starts a new device flow even when a cached token left the
// public state authorized. Preserve the stored credential until replacement
// succeeds; cancellation or refusal must not erase uninstall recovery access.
func (m *Manager) Reauthenticate(requests ...context.Context) Snapshot {
	request := m.parent
	if len(requests) > 0 {
		request = requests[0]
	}
	for !m.credentialsMu.TryLock() {
		select {
		case <-request.Done():
			return m.Snapshot()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer m.credentialsMu.Unlock()
	if request.Err() != nil {
		return m.Snapshot()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Status == "authorizing" {
		return m.state
	}
	ctx, epoch := m.resetLocked("authorizing")
	go m.authorize(ctx, epoch)
	return m.state
}

// RejectCredential invalidates only the session that made the rejected request.
// A late 401 must never sign out a newer login, even if a server reused a token.
func (m *Manager) RejectCredential(token string, epoch uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Status != "authorized" || m.state.Epoch != epoch || m.token != token {
		return
	}
	m.resetLocked("signed_out")
	m.state.Error = "session_expired"
	m.notifyLocked()
}

// CancelAuthorization drains credential writes before stopping an outstanding
// login. Uninstall must not leave a late device-flow response recreating the
// Keychain item after local credentials have been removed.
func (m *Manager) CancelAuthorization(ctx context.Context) error {
	for !m.credentialsMu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer m.credentialsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Status == "authorizing" {
		m.resetLocked("signed_out")
	}
	return nil
}
func (m *Manager) fail(epoch uint64, code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Epoch != epoch {
		return
	}
	m.state.Status = "signed_out"
	m.state.Error = code
	m.state.UserCode = ""
	m.state.URL = ""
	m.notifyLocked()
}
func (m *Manager) post(ctx context.Context, path string, body any, out any) (int, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", m.endpoint+"/v2/auth/device/"+path, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := m.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out)
}
func (m *Manager) authorize(ctx context.Context, epoch uint64) {
	var authorization struct {
		Data struct {
			DeviceCode string    `json:"device_code"`
			UserCode   string    `json:"user_code"`
			URL        string    `json:"verification_uri_complete"`
			Interval   float64   `json:"interval"`
			ExpiresAt  time.Time `json:"expires_at"`
		} `json:"data"`
	}
	status, err := m.post(ctx, "authorization", map[string]string{"client_id": "runta_crew", "device_name": "Fennec", "app_url": "https://dashboard.runta.com"}, &authorization)
	data := authorization.Data
	link, parseErr := url.Parse(data.URL)
	if err != nil || status < 200 || status >= 300 || data.DeviceCode == "" || data.UserCode == "" || parseErr != nil || link.Scheme != "https" || link.Host != "dashboard.runta.com" || link.User != nil {
		m.fail(epoch, "authorization_failed")
		return
	}
	expiry := data.ExpiresAt
	limit := time.Now().Add(10 * time.Minute)
	if expiry.IsZero() || expiry.After(limit) {
		expiry = limit
	}
	ctx, cancel := context.WithDeadline(ctx, expiry)
	defer cancel()
	m.mu.Lock()
	if m.state.Epoch != epoch {
		m.mu.Unlock()
		return
	}
	m.state.UserCode = data.UserCode
	m.state.URL = data.URL
	m.state.ExpiresAt = expiry
	m.notifyLocked()
	m.mu.Unlock()
	interval := time.Duration(data.Interval * float64(time.Second))
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			m.fail(epoch, "authorization_expired")
			return
		case <-timer.C:
		}
		var response struct {
			Token    string  `json:"access_token"`
			Error    string  `json:"error"`
			Interval float64 `json:"interval"`
		}
		status, err = m.post(ctx, "token", map[string]string{"device_code": data.DeviceCode}, &response)
		if err != nil {
			m.fail(epoch, "authorization_failed")
			return
		}
		if status >= 200 && status < 300 && response.Token != "" {
			m.credentialsMu.Lock()
			defer m.credentialsMu.Unlock()
			m.mu.Lock()
			if m.state.Epoch != epoch || ctx.Err() != nil {
				m.mu.Unlock()
				return
			}
			m.mu.Unlock()
			if err := m.credentials.Write(ctx, response.Token); err != nil {
				m.fail(epoch, "credential_save_failed")
				return
			}
			m.mu.Lock()
			m.token = response.Token
			m.state = Snapshot{Epoch: epoch, Status: "authorized"}
			m.notifyLocked()
			m.mu.Unlock()
			return
		}
		switch response.Error {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
			if hinted := time.Duration(response.Interval * float64(time.Second)); hinted > interval {
				interval = hinted
			}
		case "access_denied":
			m.fail(epoch, "access_denied")
			return
		case "expired_token":
			m.fail(epoch, "authorization_expired")
			return
		default:
			m.fail(epoch, "authorization_failed")
			return
		}
	}
}
