package account

import (
	"context"
	"encoding/json"
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
