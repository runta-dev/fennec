package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type operationResult struct {
	Agent    string            `json:"agent"`
	Location string            `json:"location"`
	Models   []json.RawMessage `json:"models"`
	Status   string            `json:"status"`
}

// Validate usable data, not merely a successful RPC envelope.
func (r operationResult) validate(method, agent, location string) error {
	if r.Agent != agent {
		return fmt.Errorf("response agent mismatch")
	}
	if method == "agent.account.get" {
		if r.Location != location {
			return fmt.Errorf("response account location mismatch")
		}
		if r.Status != "signed_in" && r.Status != "signed_out" {
			return fmt.Errorf("account state unavailable")
		}
		return nil
	}
	if len(r.Models) == 0 {
		return fmt.Errorf("model catalog unavailable")
	}
	seen := map[string]bool{}
	for _, raw := range r.Models {
		var id string
		if method == "model.local.get" {
			if json.Unmarshal(raw, &id) != nil || strings.TrimSpace(id) == "" {
				return fmt.Errorf("gateway catalog contains an unusable entry")
			}
		} else {
			var model struct {
				ID   *string `json:"id"`
				Name string  `json:"name"`
			}
			if json.Unmarshal(raw, &model) != nil || model.ID == nil || strings.TrimSpace(model.Name) == "" {
				return fmt.Errorf("model catalog contains an unusable entry")
			}
			// Empty ID is the native agent's valid Default selection.
			id = *model.ID
			if id != "" && strings.TrimSpace(id) == "" {
				return fmt.Errorf("invalid model ID")
			}
		}
		if seen[id] {
			return fmt.Errorf("model catalog contains duplicate IDs")
		}
		seen[id] = true
	}
	return nil
}
