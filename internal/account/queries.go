package account

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/launchprofile"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type httpStatusError struct {
	status     int
	code       string
	authReason string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("account HTTP %d", e.status)
}

func (m *Manager) queryContext(parent context.Context) (context.Context, string, uint64, func(), error) {
	lifecycle, token, epoch, ok := m.Session()
	if !ok {
		return nil, "", 0, func() {}, fmt.Errorf("login required")
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(lifecycle, cancel)
	return ctx, token, epoch, func() { stop(); cancel() }, nil
}
func (m *Manager) get(ctx context.Context, token, address string, epoch uint64) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := m.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		m.RejectCredential(token, epoch)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		launchprofile.Record("account.http_status."+strconv.Itoa(response.StatusCode), time.Now(), 0)
		var envelope struct {
			Error struct {
				Code                string `json:"code"`
				AuthorizationReason string `json:"authorization_reason"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&envelope)
		return nil, &httpStatusError{
			status:     response.StatusCode,
			code:       envelope.Error.Code,
			authReason: envelope.Error.AuthorizationReason,
		}
	}
	var result map[string]any
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	decoder.UseNumber()
	if err = decoder.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}
func (m *Manager) Profile(parent context.Context) (map[string]any, error) {
	ctx, token, epoch, done, err := m.queryContext(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	result, err := m.get(ctx, token, m.Endpoint()+"/v2/me", epoch)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].(map[string]any); ok {
		result = data
	}
	// Deliberately project public fields instead of forwarding arbitrary API data.
	public := map[string]any{}
	for _, field := range []string{"user_id", "display_name", "email"} {
		if value, ok := result[field].(string); ok {
			public[field] = value
		}
	}
	return public, nil
}
func (m *Manager) Billing(ctx context.Context) (map[string]any, error) {
	return m.billing(ctx, m.Endpoint())
}

func parseMinorUnits(value any) (int64, bool) {
	switch v := value.(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n, true
		}
		if f, err := v.Float64(); err == nil {
			return int64(f), true
		}
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	}
	return 0, false
}

func classifyBillingHTTPFailure(err error) (map[string]any, bool) {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return nil, false
	}
	if statusErr.status == http.StatusForbidden {
		if statusErr.authReason == "required_scope_missing" {
			return map[string]any{"reauth_required": true}, true
		}
		return map[string]any{"web_only": true}, true
	}
	if statusErr.status == http.StatusNotFound {
		return map[string]any{"web_only": true}, true
	}
	return nil, false
}

func (m *Manager) resolveBillingOrganization(ctx context.Context, token, endpoint string, epoch uint64) (string, map[string]any, error) {
	meResp, err := m.get(ctx, token, endpoint+"/v2/me", epoch)
	if err == nil {
		me, _ := meResp["data"].(map[string]any)
		if me == nil {
			me = meResp
		}
		role, _ := me["role"].(string)
		if rawScopes, hasScopes := me["scopes"].([]any); hasScopes && len(rawScopes) > 0 {
			hasBillingScope := false
			for _, item := range rawScopes {
				if s, ok := item.(string); ok && s == "billing:read" {
					hasBillingScope = true
					break
				}
			}
			if !hasBillingScope {
				if role == "developer" {
					return "", map[string]any{"web_only": true}, nil
				}
				return "", map[string]any{"reauth_required": true}, nil
			}
		}
		if role == "developer" {
			return "", map[string]any{"web_only": true}, nil
		}
		if orgID, _ := me["organization_id"].(string); strings.TrimSpace(orgID) != "" {
			return strings.TrimSpace(orgID), nil, nil
		}
	} else if fallback, ok := classifyBillingHTTPFailure(err); ok {
		return "", fallback, nil
	}

	organizations, err := m.get(ctx, token, endpoint+"/v2/organizations", epoch)
	if err != nil {
		if fallback, ok := classifyBillingHTTPFailure(err); ok {
			return "", fallback, nil
		}
		return "", nil, err
	}
	rows, ok := organizations["data"].([]any)
	if !ok || len(rows) == 0 {
		launchprofile.Record("billing.organization_count.0", time.Now(), 0)
		return "", nil, fmt.Errorf("one billing organization required")
	}
	for _, item := range rows {
		org, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if org["current"] == true {
			if id, _ := org["id"].(string); id != "" {
				return id, nil, nil
			}
		}
	}
	if len(rows) == 1 {
		if org, ok := rows[0].(map[string]any); ok {
			if id, _ := org["id"].(string); id != "" {
				return id, nil, nil
			}
		}
	}
	launchprofile.Record("billing.organization_count."+strconv.Itoa(len(rows)), time.Now(), 0)
	return "", nil, fmt.Errorf("one billing organization required")
}

func (m *Manager) billing(parent context.Context, apiOrigin string) (map[string]any, error) {
	ctx, token, epoch, done, err := m.queryContext(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	endpoint := strings.TrimRight(strings.TrimSpace(apiOrigin), "/")
	id, fallback, err := m.resolveBillingOrganization(ctx, token, endpoint, epoch)
	if err != nil {
		return nil, err
	}
	if fallback != nil {
		return fallback, nil
	}
	base := endpoint + "/v2/organizations/" + url.PathEscape(id)
	paths := []string{"/billing/account", "/billing/usage?window=30d"}
	results := make([]map[string]any, len(paths))
	errs := make([]error, len(paths))
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			response, err := m.get(ctx, token, base+path, epoch)
			if err != nil {
				errs[i] = err
				return
			}
			var valid bool
			results[i], valid = response["data"].(map[string]any)
			if !valid {
				errs[i] = fmt.Errorf("invalid billing response")
			}
		}(i, path)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			if fallback, ok := classifyBillingHTTPFailure(err); ok {
				return fallback, nil
			}
			return nil, err
		}
	}
	account, usage := results[0], results[1]
	balance, _ := account["credit_balance"].(map[string]any)
	currency, _ := account["currency"].(string)
	if currency == "" {
		currency = "USD"
	}
	result := map[string]any{"currency": currency, "stale": balance["freshness"] == "stale" || usage["freshness"] == "stale"}
	if accessState, _ := account["access_state"].(string); accessState != "" {
		result["access_state"] = accessState
	}
	if reason, _ := account["restriction_reason"].(string); reason != "" {
		result["restriction_reason"] = reason
	}
	if balance["freshness"] != "unavailable" {
		if amount, ok := parseMinorUnits(balance["amount_minor"]); ok {
			result["balance"] = amount
		}
	}
	if usage["freshness"] != "unavailable" {
		if breakdown, ok := usage["breakdown"].([]any); ok {
			var total int64
			for _, row := range breakdown {
				if item, ok := row.(map[string]any); ok {
					if amount, ok := parseMinorUnits(item["amount_minor"]); ok {
						total += amount
					}
				}
			}
			result["spend"] = total
		}
		quantities, _ := usage["quantities"].([]any)
		for _, measure := range []struct{ field, metric, unit string }{{"cpu", "vcpu_hours", "vCPU-h"}, {"memory", "memory_gib_hours", "GiB-h"}, {"disk", "disk_gib_hours", "GiB-h"}} {
			total := float64(0)
			for _, row := range quantities {
				item, ok := row.(map[string]any)
				if !ok {
					continue
				}
				metric, _ := item["metric"].(string)
				if metric != measure.metric && metric != "runta_"+measure.metric {
					continue
				}
				text, _ := item["quantity_decimal"].(string)
				quantity, err := strconv.ParseFloat(text, 64)
				if err != nil {
					continue
				}
				total += quantity
			}
			// When freshness is fresh or stale, an omitted metric row represents 0.00
			// billable usage rather than missing data. Only freshness=="unavailable"
			// leaves the field unset.
			result[measure.field] = fmt.Sprintf("%.2f %s", total, measure.unit)
		}
	}
	return result, nil
}
