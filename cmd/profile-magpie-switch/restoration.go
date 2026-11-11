package main

import (
	"encoding/json"
	"fmt"
)

type routeState struct {
	Agent  string `json:"agent"`
	Status string `json:"status"`
	Model  string `json:"model,omitempty"`
}

func routeBaseline(state snapshot) (map[string]routeState, error) {
	event := state.Runtime.Events["route_results"]
	if state.Runtime.RuntimeID == "" || event["runtime_id"] != state.Runtime.RuntimeID {
		return nil, fmt.Errorf("route runtime mismatch")
	}
	var routes []routeState
	if json.Unmarshal([]byte(event["results"]), &routes) != nil {
		return nil, fmt.Errorf("invalid route baseline")
	}
	expected := map[string]bool{"claude": true, "codex": true, "pi": true}
	result := map[string]routeState{}
	for _, route := range routes {
		if !expected[route.Agent] || route.Status == "" {
			return nil, fmt.Errorf("incomplete or duplicate route baseline")
		}
		delete(expected, route.Agent)
		result[route.Agent] = route
	}
	if len(expected) != 0 {
		return nil, fmt.Errorf("incomplete route baseline")
	}
	return result, nil
}

// Restoration preserves the observed starting state, including an existing
// signed-out/local-only provider. It must not claim those agents became usable.
func validateRestoration(current, initial snapshot) error {
	if !settled(current.Runtime) || current.Runtime.RuntimeID != initial.Runtime.RuntimeID {
		return fmt.Errorf("restored runtime is not settled")
	}
	before, err := routeBaseline(initial)
	if err != nil {
		return err
	}
	after, err := routeBaseline(current)
	if err != nil {
		return err
	}
	for agent, want := range before {
		if after[agent] != want {
			return fmt.Errorf("restored route differs from initial baseline: %s", agent)
		}
	}
	if initial.Settings.Magpie {
		return validateCompletion(current, true)
	}
	return nil
}
