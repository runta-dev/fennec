package main

import "encoding/json"

// Only known numeric timing fields enter evidence, never arbitrary event data.
func phaseEvidence(state snapshot) any {
	event := state.Runtime.Events["configuration_profile"]
	if event["runtime_id"] != state.Runtime.RuntimeID {
		return nil
	}
	var raw map[string]map[string]float64
	if json.Unmarshal([]byte(event["timings"]), &raw) != nil {
		return nil
	}
	allowed := map[string][]string{
		"local_ms":  {"transport_ready", "native_accounts", "local_routes", "plugins", "gateway_accounts", "local_prepare_wall", "remote_round_trip", "transport_request_write", "transport_response_wait", "transport_remote_operation", "publish"},
		"remote_ms": {"tool_preflight", "catalog_preflight", "accounts", "routes", "catalog_readback"},
	}
	result := map[string]map[string]float64{}
	for group, fields := range allowed {
		result[group] = map[string]float64{}
		for _, key := range fields {
			if value, ok := raw[group][key]; ok && value >= 0 {
				result[group][key] = value
			}
		}
	}
	return result
}
