package cli

import (
	"context"
	"encoding/json"
	"path/filepath"

	"fennec/internal/audit"
	"fennec/internal/control"
	"fennec/internal/localssh"
)

func connectorDirectory(home string) string { return filepath.Join(home, ".config/fennec/ssh") }

// queryAudit marks refused commands the user has since allowed, so the
// Activity row shows "Allowed" instead of offering the switch again.
func queryAudit(home string, store *audit.Store, opts audit.QueryOptions) (audit.QueryResult, error) {
	result, err := store.Query(opts)
	if err != nil {
		return result, err
	}
	directory := connectorDirectory(home)
	for i := range result.Entries {
		if grant := result.Entries[i].Grant; grant != "" {
			result.Entries[i].Allowed = localssh.Granted(directory, grant)
		}
	}
	return result, nil
}

// allowAudit lets the user overrule an auto-review refusal from Activity.
// The grant covers that exact command from that paired device.
func allowAudit(home string, store *audit.Store) func(context.Context, json.RawMessage) (any, error) {
	return func(_ context.Context, raw json.RawMessage) (any, error) {
		var params struct {
			ID     string `json:"id"`
			Filter string `json:"filter"`
		}
		if json.Unmarshal(raw, &params) != nil || params.ID == "" {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid activity entry"}
		}
		entry, found := store.Find(params.ID)
		if !found || entry.Grant == "" || !localssh.Grantable(entry.Decision) {
			return nil, &control.Fault{Code: "not_allowable", Message: "This activity cannot be allowed"}
		}
		if err := localssh.AllowGrant(connectorDirectory(home), entry.Grant); err != nil {
			return nil, err
		}
		return queryAudit(home, store, audit.QueryOptions{Filter: params.Filter, Limit: 100})
	}
}
