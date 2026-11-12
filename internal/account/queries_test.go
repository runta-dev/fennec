package account

import (
	"context"
	"encoding/json"
	"fmt"

	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBillingAggregationAndPublicProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private" {
			t.Error("missing daemon credential")
		}
		var data any
		switch r.URL.Path {
		case "/v2/me":
			data = map[string]any{"user_id": "owner", "display_name": "Name", "access_token": "do-not-forward"}
		case "/v1/organizations":
			data = []any{map[string]any{"id": "org"}}
		case "/v1/organizations/org/billing/account":
			data = map[string]any{"currency": "USD"}
		case "/v1/organizations/org/billing":
			data = map[string]any{"credit_balance": map[string]any{"amount_minor": 123, "freshness": "stale"}}
		case "/v1/organizations/org/billing/usage":
			data = map[string]any{"freshness": "fresh", "breakdown": []any{map[string]any{"amount_minor": 20}, map[string]any{"amount_minor": 30}}, "quantities": []any{map[string]any{"metric": "runta_vcpu_hours", "quantity_decimal": "1.25"}}}
		default:
			t.Error("unexpected path", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, &memoryCredentials{token: "private"}, server.URL)
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	profile, err := m.Profile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := profile["access_token"]; found {
		t.Fatal("unprojected credential field")
	}
	result, err := m.billing(ctx, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if result["balance"] != float64(123) || result["spend"] != float64(50) || result["cpu"] != "1.25 vCPU-h" || result["stale"] != true {
		t.Fatal(result)
	}
}

func TestOnlyUnauthorizedResponseReturnsAccountToSignIn(t *testing.T) {
	for _, status := range []int{401, 403, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); fmt.Fprint(w, `{}`) }))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &memoryCredentials{token: "fixture-private"}
			m := New(ctx, store, server.URL)
			if err := m.Restore(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Profile(ctx); err == nil {
				t.Fatal("API failure ignored")
			}
			state := m.Snapshot()
			if status == 401 {
				if state.Status != "signed_out" || state.Error != "session_expired" {
					t.Fatal("expired credential trapped in authorized state", state)
				}
			} else if state.Status != "authorized" {
				t.Fatal("permission/server failure logged user out", state)
			}
			if token, _ := store.Read(ctx); token != "fixture-private" {
				t.Fatal("stored recovery credential erased")
			}
		})
	}
}
func TestLateUnauthorizedResponseCannotSignOutNewLogin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &memoryCredentials{token: "same-fixture-token"}
	m := New(ctx, store, "")
	_ = m.Restore(ctx)
	_, token, epoch, _ := m.Session()
	if err := m.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	store.Write(ctx, token)
	_ = m.Restore(ctx)
	m.RejectCredential(token, epoch)
	if m.Snapshot().Status != "authorized" {
		t.Fatal("old request invalidated a newer epoch using the same token")
	}
}

func TestBillingForbiddenReturnsWebDashboardFallbackInsteadOfRetryError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/organizations" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "org"}}})
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, &memoryCredentials{token: "private"}, server.URL)
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := m.billing(ctx, server.URL)
	if err != nil {
		t.Fatalf("expected structured web_only fallback for HTTP 403, got err=%v", err)
	}
	if result["web_only"] != true || m.Snapshot().Status != "authorized" {
		t.Fatalf("unexpected 403 billing result: %+v", result)
	}
}
