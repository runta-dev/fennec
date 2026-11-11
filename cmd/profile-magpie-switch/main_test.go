package main

import (
	"encoding/json"
	"testing"

	"fennec/internal/controlplane"
)

func readyFixture(magpie bool) snapshot {
	s := snapshot{Runtime: controlplane.RuntimeState{Generation: 2, Stage: "ready", RuntimeID: "fixture", Events: map[string]map[string]string{}}}
	s.Runtime.Events["route_results"] = map[string]string{"runtime_id": "fixture", "results": `[{"agent":"codex","status":"configured"},{"agent":"claude","status":"configured"},{"agent":"pi","status":"configured"}]`}
	if magpie {
		s.Runtime.Events["model_catalog"] = map[string]string{"runtime_id": "fixture", "models": `["provider/model"]`}
	}
	return s
}
func TestCompletionRejectsReceiptsAndStaleData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*snapshot)
	}{
		{"running", func(s *snapshot) { s.Runtime.Running = true }},
		{"queued", func(s *snapshot) { s.Runtime.Pending = true }},
		{"failed", func(s *snapshot) { s.Runtime.Error = "failed" }},
		{"wrong runtime", func(s *snapshot) { s.Runtime.Events["route_results"]["runtime_id"] = "other" }},
		{"missing routes", func(s *snapshot) { delete(s.Runtime.Events, "route_results") }},
		{"duplicate routes", func(s *snapshot) {
			s.Runtime.Events["route_results"]["results"] = `[{"agent":"codex","status":"configured"},{"agent":"codex","status":"configured"}]`
		}},
		{"missing catalog", func(s *snapshot) { delete(s.Runtime.Events, "model_catalog") }},
		{"empty catalog", func(s *snapshot) { s.Runtime.Events["model_catalog"]["models"] = `[]` }},
		{"stale catalog", func(s *snapshot) { s.Runtime.Events["model_catalog"]["runtime_id"] = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := readyFixture(true)
			tc.mutate(&s)
			if validateCompletion(s, true) == nil {
				t.Fatal("false completion")
			}
		})
	}
}
func TestNativeSignedOutIsNormalButGatewayRequiresRoutes(t *testing.T) {
	s := readyFixture(false)
	raw, _ := json.Marshal([]map[string]string{{"agent": "codex", "status": "configured"}, {"agent": "claude", "status": "credentials_required"}, {"agent": "pi", "status": "credentials_required"}})
	s.Runtime.Events["route_results"]["results"] = string(raw)
	if err := validateCompletion(s, false); err != nil {
		t.Fatal(err)
	}
	if validateCompletion(s, true) == nil {
		t.Fatal("gateway accepted native signed-out routes")
	}
	if err := validateCompletion(readyFixture(true), true); err != nil {
		t.Fatal(err)
	}
}
