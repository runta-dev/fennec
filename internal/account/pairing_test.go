package account

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func pairingServer(t *testing.T, organizations []string, approveStatus int, approved *map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/auth/device/authorization":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["client_id"] != "runta_crew" || body["device_name"] != "Fennec on iPhone" {
				t.Errorf("authorization body = %v", body)
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"device_code": "device-secret", "user_code": "ABCD-EFGH",
				"verification_uri_complete": "https://dashboard.runta.com/device?code=ABCDEFGH",
				"expires_at":                time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
			}})
		case "/v1/auth/device/authorization":
			if r.Header.Get("Authorization") != "Bearer mac-token" || r.URL.Query().Get("code") != "ABCDEFGH" {
				t.Errorf("lookup auth=%q code=%q", r.Header.Get("Authorization"), r.URL.Query().Get("code"))
			}
			rows := []map[string]string{}
			for _, id := range organizations {
				rows = append(rows, map[string]string{"id": id, "name": id})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"organizations": rows}})
		case "/v2/me":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"user_id": "u", "organization_id": "org-b"}})
		case "/v1/auth/device/authorization/approve":
			if approveStatus != 0 {
				w.WriteHeader(approveStatus)
				return
			}
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			*approved = body
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
}

func signedIn(t *testing.T, endpoint string) (*Manager, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	m := New(ctx, &memoryCredentials{token: "mac-token"}, endpoint)
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	return m, cancel
}

func TestPairDeviceApprovesAndReturnsDeviceCodeURL(t *testing.T) {
	var approved map[string]string
	server := pairingServer(t, []string{"org-a"}, 0, &approved)
	defer server.Close()
	m, cancel := signedIn(t, server.URL)
	defer cancel()
	pairing, err := m.PairDevice(context.Background(), "Fennec on iPhone")
	if err != nil {
		t.Fatal(err)
	}
	if approved["code"] != "ABCDEFGH" || approved["organization_id"] != "org-a" {
		t.Fatalf("approve body = %v", approved)
	}
	code, api, err := ParsePairingURL(pairing.URL)
	if err != nil || code != "device-secret" || api != server.URL {
		t.Fatalf("url %q -> %q %q %v", pairing.URL, code, api, err)
	}
	if strings.Contains(pairing.URL, "?") {
		t.Fatal("device code must stay in the fragment")
	}
	if time.Until(pairing.ExpiresAt) <= 0 || time.Until(pairing.ExpiresAt) > 10*time.Minute {
		t.Fatalf("expiry %v", pairing.ExpiresAt)
	}
}

func TestPairDevicePicksCurrentOrganization(t *testing.T) {
	var approved map[string]string
	server := pairingServer(t, []string{"org-a", "org-b"}, 0, &approved)
	defer server.Close()
	m, cancel := signedIn(t, server.URL)
	defer cancel()
	if _, err := m.PairDevice(context.Background(), "Fennec on iPhone"); err != nil {
		t.Fatal(err)
	}
	if approved["organization_id"] != "org-b" {
		t.Fatalf("approved for %q", approved["organization_id"])
	}
}

func TestPairDeviceRefusalKeepsMacSignedIn(t *testing.T) {
	var approved map[string]string
	server := pairingServer(t, []string{"org-a"}, http.StatusForbidden, &approved)
	defer server.Close()
	m, cancel := signedIn(t, server.URL)
	defer cancel()
	if _, err := m.PairDevice(context.Background(), "Fennec on iPhone"); !errors.Is(err, ErrPairingNotAllowed) {
		t.Fatalf("err = %v", err)
	}
	if m.Snapshot().Status != "authorized" {
		t.Fatalf("status = %s", m.Snapshot().Status)
	}
}

func TestParsePairingURLRejectsOtherLinks(t *testing.T) {
	for _, raw := range []string{"https://example.com#code=x&api=y", "fennec://pair#code=x", "fennec://pair?code=x&api=y"} {
		if _, _, err := ParsePairingURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
