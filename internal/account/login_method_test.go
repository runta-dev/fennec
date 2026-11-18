package account

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestLoginMethodsPreserveDeviceFlow(t *testing.T) {
	for _, method := range []string{"google", "github", "email", ""} {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/auth/device/authorization" {
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
					"device_code": "private-device-secret", "user_code": "ABCD-EFGH",
					"verification_uri_complete": "https://dashboard.runta.xyz/device?code=ABCD-EFGH",
					"expires_at":                time.Now().Add(time.Minute), "interval": 5,
				}})
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := New(ctx, &memoryCredentials{}, server.URL)
			if err := m.SetEndpoint(ctx, server.URL, "https://dashboard.runta.xyz"); err != nil {
				t.Fatal(err)
			}
			if _, err := m.LoginMethod(method); err != nil {
				t.Fatal(err)
			}
			deadline := time.After(time.Second)
			for {
				changed := m.Changes()
				snapshot := m.Snapshot()
				if snapshot.URL != "" {
					link, err := url.Parse(snapshot.URL)
					if err != nil {
						t.Fatal(err)
					}
					if link.Host != "dashboard.runta.xyz" || link.Path != "/device" || link.Query().Get("code") != "ABCD-EFGH" || link.Query().Get("method") != method {
						t.Fatal(snapshot.URL)
					}
					if err := m.CancelAuthorization(ctx); err != nil {
						t.Fatal(err)
					}
					if m.Snapshot().Status != "signed_out" {
						t.Fatal(m.Snapshot())
					}
					return
				}
				select {
				case <-changed:
				case <-deadline:
					t.Fatal("no verification URL", snapshot)
				}
			}
		})
	}
}

func TestUnsupportedLoginMethodDoesNotStartAuthorization(t *testing.T) {
	m := New(context.Background(), &memoryCredentials{}, "https://api.runta.com")
	for _, method := range []string{"Google", "apple", "https://attacker.example", "google&redirect=evil"} {
		if _, err := m.LoginMethod(method); err == nil {
			t.Fatalf("accepted %q", method)
		}
		if m.Snapshot().Status != "signed_out" {
			t.Fatal(m.Snapshot())
		}
	}
}
