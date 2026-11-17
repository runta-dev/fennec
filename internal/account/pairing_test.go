package account

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func signedIn(t *testing.T, endpoint string) (*Manager, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	m := New(ctx, &memoryCredentials{token: "mac-token"}, endpoint)
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	return m, cancel
}

func TestPairDeviceReturnsQRAndApprovalLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/auth/device/authorization" {
			t.Errorf("unexpected path %s", r.URL.Path)
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["client_id"] != "runta_crew" || body["device_name"] != "Fennec on iPhone" {
			t.Errorf("authorization body = %v", body)
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"device_code": "device-secret", "user_code": "ABCD-EFGH",
			"verification_uri_complete": "https://dashboard.runta.com/device?code=ABCD-EFGH",
			"expires_at":                time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339),
		}})
	}))
	defer server.Close()
	m, cancel := signedIn(t, server.URL)
	defer cancel()
	pairing, err := m.PairDevice(context.Background(), "Fennec on iPhone")
	if err != nil {
		t.Fatal(err)
	}
	code, api, err := ParsePairingURL(pairing.URL)
	if err != nil || code != "device-secret" || api != server.URL {
		t.Fatalf("url %q -> %q %q %v", pairing.URL, code, api, err)
	}
	if strings.Contains(pairing.URL, "?") {
		t.Fatal("device code must stay in the fragment")
	}
	if pairing.ApproveURL != "https://dashboard.runta.com/device?code=ABCD-EFGH" {
		t.Fatalf("approve url %q", pairing.ApproveURL)
	}
}

func TestPairDeviceRejectsForeignApprovalHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"device_code": "d", "verification_uri_complete": "https://evil.example/device?code=X"}})
	}))
	defer server.Close()
	m, cancel := signedIn(t, server.URL)
	defer cancel()
	if _, err := m.PairDevice(context.Background(), "Fennec on iPhone"); err == nil {
		t.Fatal("accepted a foreign approval link")
	}
}

func TestPairDeviceReportsRateLimitServedAs500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":{"code":"internal","message":"internal server error"}}`))
	}))
	defer server.Close()
	m, cancel := signedIn(t, server.URL)
	defer cancel()
	if _, err := m.PairDevice(context.Background(), "Fennec on iPhone"); !errors.Is(err, ErrPairingRateLimited) {
		t.Fatalf("err = %v", err)
	}
	if m.Snapshot().Status != "authorized" {
		t.Fatal("a refused pairing must not sign the Mac out")
	}
}

func TestRedeemPollsUntilApproved(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v2/auth/device/token" || body["device_code"] != "device-secret" {
			t.Errorf("unexpected %s %v", r.URL.Path, body)
		}
		if polls.Add(1) < 2 {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "phone-token"})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &memoryCredentials{}
	m := New(ctx, store, server.URL)
	if state := m.Redeem("device-secret"); state.Status != "authorizing" {
		t.Fatalf("status %s", state.Status)
	}
	deadline := time.Now().Add(20 * time.Second)
	for m.Snapshot().Status != "authorized" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	stored, _ := store.Read(context.Background())
	if token, _ := m.Token(); token != "phone-token" || stored != "phone-token" {
		t.Fatalf("token %q stored %q status %s", token, stored, m.Snapshot().Status)
	}
}

func TestParsePairingURLRejectsOtherLinks(t *testing.T) {
	for _, raw := range []string{"https://example.com#code=x&api=y", "fennec://pair#code=x", "fennec://pair?code=x&api=y"} {
		if _, _, err := ParsePairingURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
