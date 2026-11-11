package account

import (
	"context"
	"encoding/json"
	"errors"

	"fmt"
	"sync/atomic"

	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryCredentials struct {
	mu    sync.Mutex
	token string
}

func (c *memoryCredentials) Read(context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" {
		return "", ErrNotFound
	}
	return c.token, nil
}
func (c *memoryCredentials) Write(_ context.Context, s string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = s
	return nil
}
func (c *memoryCredentials) Delete(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	return nil
}
func TestRestoreNeverPublishesToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	credentials := &memoryCredentials{token: "secret-token"}
	m := New(ctx, credentials, "")
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m.Snapshot())
	if strings.Contains(string(data), "secret-token") {
		t.Fatal("token leaked")
	}
	if m.Snapshot().Status != "authorized" {
		t.Fatal(m.Snapshot())
	}
	old := m.Snapshot().Epoch
	if err := m.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if token, _ := m.Token(); token != "" {
		t.Fatal("logout retained token")
	}
	if m.Snapshot().Epoch <= old {
		t.Fatal("epoch not advanced")
	}
}
func TestExpiryDoesNotWaitForPollInterval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			t.Error("expired flow polled token")
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"device_code": "private", "user_code": "public", "verification_uri_complete": "https://dashboard.runta.com/authorize", "expires_at": time.Now().Add(100 * time.Millisecond), "interval": 600}})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, &memoryCredentials{}, server.URL)
	m.Login()
	deadline := time.After(2 * time.Second)
	for {
		changed := m.Changes()
		s := m.Snapshot()
		if s.Error == "authorization_expired" {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("expiry waited for poll", s)
		}
	}
}
func TestLogoutIgnoresLateAuthorization(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"device_code": "private", "user_code": "public", "verification_uri_complete": "https://dashboard.runta.com/authorize"}})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, &memoryCredentials{}, server.URL)
	m.Login()
	<-entered
	if err := m.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	if s := m.Snapshot(); s.Status != "signed_out" || s.URL != "" {
		t.Fatal(s)
	}
}

type waitingCredentials struct{ entered, release chan struct{} }

func (c waitingCredentials) Read(ctx context.Context) (string, error) {
	close(c.entered)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-c.release:
		return "restored", nil
	}
}
func (waitingCredentials) Write(context.Context, string) error { return nil }
func (waitingCredentials) Delete(context.Context) error        { return nil }
func TestCredentialRestoreDoesNotBlockPublicState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	credentials := waitingCredentials{make(chan struct{}), make(chan struct{})}
	m := New(ctx, credentials, "")
	done := make(chan error, 1)
	go func() { done <- m.Restore(ctx) }()
	<-credentials.entered
	state := make(chan Snapshot, 1)
	go func() { state <- m.Snapshot() }()
	select {
	case snapshot := <-state:
		if snapshot.Status != "restoring" {
			t.Fatal(snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("public state blocked on Keychain")
	}
	close(credentials.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReauthenticationReplacesCachedTokenOnlyAfterSuccess(t *testing.T) {
	entered := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/authorization") {
			if requests.Add(1) == 1 {
				close(entered)
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"device_code": "fixture-code", "user_code": "VISIBLE", "verification_uri_complete": "https://dashboard.runta.com/authorize", "expires_at": time.Now().Add(time.Minute), "interval": 5}})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "new-fixture-token"})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := &memoryCredentials{token: "old-fixture-token"}
	m := New(ctx, store, server.URL)
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	oldCtx, _, epoch, _ := m.Session()
	if got := m.Reauthenticate(); got.Status != "authorizing" || got.Epoch <= epoch {
		t.Fatal("cached authorized state prevented reauthentication")
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("device flow did not start")
	}
	if oldCtx.Err() == nil {
		t.Fatal("old account lifecycle still active")
	}
	if token, _ := store.Read(ctx); token != "old-fixture-token" {
		t.Fatal("old credential erased before replacement")
	}
	for i := 0; i < 5; i++ {
		m.Reauthenticate()
	}
	for {
		changed := m.Changes()
		if m.Snapshot().Status == "authorized" {
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatal("replacement login did not finish")
		}
	}
	if requests.Load() != 1 {
		t.Fatal("duplicate device authorizations")
	}
	if token, _ := store.Read(ctx); token != "new-fixture-token" {
		t.Fatal("replacement credential not saved")
	}
	public, _ := json.Marshal(m.Snapshot())
	if strings.Contains(string(public), "fixture-token") {
		t.Fatal("token exposed in public state")
	}
}
func TestRejectedReauthenticationPreservesStoredCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403); fmt.Fprint(w, `{}`) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &memoryCredentials{token: "recovery-fixture"}
	m := New(ctx, store, server.URL)
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	m.Reauthenticate()
	for {
		changed := m.Changes()
		if m.Snapshot().Status == "signed_out" {
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatal("rejected login did not settle")
		}
	}
	if token, _ := store.Read(ctx); token != "recovery-fixture" {
		t.Fatal("recovery credential deleted")
	}
}

func TestUninstallCancelsPendingAuthorizationWithoutDeletingStoredToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			t.Error("cancelled authorization still requested a token")
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"device_code": "fixture", "user_code": "VISIBLE", "verification_uri_complete": "https://dashboard.runta.com/authorize", "interval": 5}})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &memoryCredentials{token: "recovery"}
	m := New(ctx, store, server.URL)
	_ = m.Restore(ctx)
	m.Reauthenticate()
	old, _, _, _ := m.Session()
	if err := m.CancelAuthorization(ctx); err != nil {
		t.Fatal(err)
	}
	if old.Err() == nil || m.Snapshot().Status != "signed_out" {
		t.Fatal("authorization survived uninstall barrier")
	}
	if token, _ := store.Read(ctx); token != "recovery" {
		t.Fatal("uninstall barrier erased the credential")
	}
}
func TestCredentialWriteDrainHonorsUninstallDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, &memoryCredentials{token: "fixture"}, "")
	_ = m.Restore(ctx)
	m.credentialsMu.Lock()
	defer m.credentialsMu.Unlock()
	deadline, stop := context.WithTimeout(ctx, 25*time.Millisecond)
	defer stop()
	if err := m.CancelAuthorization(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("credential write drain ignored deadline", err)
	}
	if m.Snapshot().Status != "authorized" {
		t.Fatal("failed quiesce changed account state")
	}
}
