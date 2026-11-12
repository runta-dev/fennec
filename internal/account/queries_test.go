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
			data = map[string]any{"user_id": "owner", "display_name": "Name", "organization_id": "org", "role": "owner", "scopes": []string{"runtimes:write", "billing:read"}, "access_token": "do-not-forward"}
		case "/v2/organizations/org/billing/account":
			data = map[string]any{"currency": "USD", "access_state": "active", "credit_balance": map[string]any{"amount_minor": 123, "freshness": "stale"}}
		case "/v2/organizations/org/billing/usage":
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
	if result["balance"] != int64(123) || result["spend"] != int64(50) || result["cpu"] != "1.25 vCPU-h" || result["stale"] != true {
		t.Fatal(result)
	}
	if result["memory"] != "0.00 GiB-h" || result["disk"] != "0.00 GiB-h" {
		t.Fatalf("expected omitted fresh metrics to report 0.00 instead of missing: %+v", result)
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
		if r.URL.Path == "/v2/me" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"user_id": "u1", "organization_id": "org", "role": "admin", "scopes": []string{"billing:read"}}})
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

func TestBillingDetectsMissingScopeForReauthAndScopedCredentialsIsolation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/me" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"user_id": "u1", "organization_id": "org", "role": "owner", "scopes": []string{"runtimes:write"}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activeScope := "https://api.runta.com"
	base := &memoryCredentials{token: "prod-legacy-token"}
	scoped := &ScopedCredentials{Base: base, Scope: func() string { return activeScope }}

	m := New(ctx, scoped, "https://api.runta.com")
	if err := m.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if tok, _ := m.Token(); tok != "prod-legacy-token" {
		t.Fatalf("expected legacy production token, got %q", tok)
	}

	// Switch to integration: signed_out initially while production remains intact.
	activeScope = "https://api.runta.xyz"
	if err := m.SetEndpoint(ctx, "https://api.runta.xyz", "https://dashboard.runta.xyz"); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().Status != "signed_out" || m.DashboardURL() != "https://dashboard.runta.xyz" {
		t.Fatalf("expected integration to start signed_out with xyz dashboard, got %+v %s", m.Snapshot(), m.DashboardURL())
	}

	// Save an integration token, then verify switching back restores production token!
	if err := scoped.Write(ctx, "integ-token"); err != nil {
		t.Fatal(err)
	}
	activeScope = server.URL
	if err := scoped.Write(ctx, "old-scope-token"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEndpoint(ctx, server.URL, "https://dashboard.runta.xyz"); err != nil {
		t.Fatal(err)
	}
	billingRes, err := m.Billing(ctx)
	if err != nil || billingRes["reauth_required"] != true {
		t.Fatalf("expected reauth_required for legacy token missing billing:read, got %+v err=%v", billingRes, err)
	}

	activeScope = "https://api.runta.com"
	if err := m.SetEndpoint(ctx, "https://api.runta.com", "https://dashboard.runta.com"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := m.Token(); tok != "prod-legacy-token" {
		t.Fatalf("switching back to production lost original token: %q", tok)
	}
}
