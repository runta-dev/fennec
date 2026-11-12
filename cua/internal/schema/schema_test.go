package schema

import (
	"encoding/json"
	"testing"

	"fennec/cua/internal/protocol"
)

func TestActSchemaShape(t *testing.T) {
	var s map[string]any
	if err := json.Unmarshal(For(protocol.ActRequest{}), &s); err != nil {
		t.Fatal(err)
	}
	if s["type"] != "object" || s["additionalProperties"] != false {
		t.Fatalf("top %v", s)
	}
	props := s["properties"].(map[string]any)
	steps := props["steps"].(map[string]any)
	step := steps["items"].(map[string]any)["properties"].(map[string]any)
	if _, ok := step["press"].(map[string]any)["anyOf"]; !ok {
		t.Errorf("press is not a target: %v", step["press"])
	}
	req, _ := s["required"].([]any)
	if len(req) != 1 || req[0] != "steps" {
		t.Errorf("required %v", req)
	}
	wait := step["wait"].(map[string]any)["properties"].(map[string]any)
	if _, ok := wait["window_opens"]; !ok {
		t.Error("embedded Expect fields missing from wait")
	}
}

func TestEveryToolSchemaMarshals(t *testing.T) {
	for _, v := range []any{protocol.ObserveRequest{}, protocol.ActRequest{}, protocol.WaitRequest{}, protocol.RunRequest{}, protocol.FlowRequest{}} {
		if !json.Valid(For(v)) {
			t.Errorf("%T", v)
		}
	}
}
