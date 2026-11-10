package runta

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// MergeProviderBinding preserves opaque existing fields, including future API
// additions. Conflicting same-host credentials require explicit reconciliation.
func MergeProviderBinding(existing []json.RawMessage, p ModelProvider) ([]json.RawMessage, bool, error) {
	if err := ValidateGatewayProvider(p); err != nil {
		return nil, false, err
	}
	u, _ := url.Parse(p.BaseURL)
	host := u.Hostname()
	path := strings.TrimRight(u.EscapedPath(), "/") + "/*"
	if path == "/*" {
		return nil, false, fmt.Errorf("provider base path must scope credential injection")
	}
	rule := map[string]any{"host_pattern": host, "path_pattern": path, "action": "header", "name": p.Header, "value_template": p.Template}
	out := append([]json.RawMessage(nil), existing...)
	index := -1
	for i, raw := range existing {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, false, err
		}
		var secret string
		if err := json.Unmarshal(item["secret_id"], &secret); err != nil {
			return nil, false, err
		}
		if secret == p.SecretID {
			if index != -1 {
				return nil, false, fmt.Errorf("duplicate secret configuration")
			}
			index = i
		}
		var rules []map[string]any
		if r, ok := item["egress_rules"]; ok {
			if err := json.Unmarshal(r, &rules); err != nil {
				return nil, false, err
			}
		}
		for _, r := range rules {
			if r["host_pattern"] == host && r["action"] == "header" && strings.EqualFold(fmt.Sprint(r["name"]), p.Header) {
				if secret == p.SecretID && r["path_pattern"] == path && r["value_template"] == p.Template {
					return out, false, nil
				}
				return nil, false, fmt.Errorf("existing credential rule for provider host requires reconciliation")
			}
		}
	}
	if index >= 0 {
		var item map[string]json.RawMessage
		json.Unmarshal(existing[index], &item)
		var rules []map[string]any
		if value, ok := item["egress_rules"]; ok {
			json.Unmarshal(value, &rules)
		}
		rules = append(rules, rule)
		item["egress_rules"], _ = json.Marshal(rules)
		out[index], _ = json.Marshal(item)
	} else {
		raw, _ := json.Marshal(map[string]any{"secret_id": p.SecretID, "egress_rules": []any{rule}})
		out = append(out, raw)
	}
	return out, true, nil
}

// BindProvider references an already selected tenant secret; no secret values
// are fetched. The revision guard prevents overwriting concurrent configuration.
func (c *Client) BindProvider(ctx context.Context, runtimeID string, p ModelProvider) error {
	current, err := c.Get(ctx, runtimeID)
	if err != nil {
		return err
	}
	user, err := c.CurrentUser(ctx)
	if err != nil {
		return err
	}
	if current.Owner != user || current.Environment["FENNEC_PURPOSE"] != "shared-v1" {
		return fmt.Errorf("not this user's Fennec runtime")
	}
	merged, changed, err := MergeProviderBinding(current.Secrets, p)
	if err != nil || !changed {
		return err
	}
	if current.Revision == 0 {
		return fmt.Errorf("runtime revision required")
	}
	return c.request(ctx, "PATCH", fmt.Sprintf("/v2/runtimes/%s?expected_revision=%d", url.PathEscape(runtimeID), current.Revision), map[string]any{"secret_configuration": merged}, "", nil)
}
