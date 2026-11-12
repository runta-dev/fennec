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
	"sync"
	"time"
)

type httpStatusError struct {
	status int
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
		return nil, &httpStatusError{status: response.StatusCode}
	}
	var result map[string]any
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result); err != nil {
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
	result, err := m.get(ctx, token, m.endpoint+"/v2/me", epoch)
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
	return m.billing(ctx, "https://backend.runta.com")
}
func (m *Manager) billing(parent context.Context, backend string) (map[string]any, error) {
	ctx, token, epoch, done, err := m.queryContext(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	organizations, err := m.get(ctx, token, backend+"/v1/organizations", epoch)
	if err != nil {
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusForbidden {
			return map[string]any{"web_only": true}, nil
		}
		return nil, err
	}
	rows, ok := organizations["data"].([]any)
	if !ok || len(rows) != 1 {
		launchprofile.Record("billing.organization_count."+strconv.Itoa(len(rows)), time.Now(), 0)
		return nil, fmt.Errorf("one billing organization required")
	}
	org, ok := rows[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid billing organization")
	}
	id, ok := org["id"].(string)
	if !ok || id == "" {
		return nil, fmt.Errorf("missing billing organization")
	}
	base := backend + "/v1/organizations/" + url.PathEscape(id)
	paths := []string{"/billing/account", "/billing", "/billing/usage?window=30d"}
	results := make([]map[string]any, len(paths))
	errs := make([]error, len(paths))
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func() {
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
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			var statusErr *httpStatusError
			if errors.As(err, &statusErr) && statusErr.status == http.StatusForbidden {
				return map[string]any{"web_only": true}, nil
			}
			return nil, err
		}
	}
	account, billing, usage := results[0], results[1], results[2]
	balance, _ := billing["credit_balance"].(map[string]any)
	currency, _ := account["currency"].(string)
	if currency == "" {
		currency = "USD"
	}
	result := map[string]any{"currency": currency, "stale": balance["freshness"] == "stale" || usage["freshness"] == "stale"}
	if balance["freshness"] != "unavailable" {
		if amount, ok := balance["amount_minor"].(float64); ok {
			result["balance"] = amount
		}
	}
	if usage["freshness"] != "unavailable" {
		if breakdown, ok := usage["breakdown"].([]any); ok {
			total := float64(0)
			for _, row := range breakdown {
				if item, ok := row.(map[string]any); ok {
					amount, _ := item["amount_minor"].(float64)
					total += amount
				}
			}
			result["spend"] = total
		}
		quantities, _ := usage["quantities"].([]any)
		for _, measure := range []struct{ field, metric, unit string }{{"cpu", "vcpu_hours", "vCPU-h"}, {"memory", "memory_gib_hours", "GiB-h"}, {"disk", "disk_gib_hours", "GiB-h"}} {
			total, found := float64(0), false
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
				found = true
			}
			if found {
				result[measure.field] = fmt.Sprintf("%.2f %s", total, measure.unit)
			}
		}
	}
	return result, nil
}
