package runta

import (
	"encoding/json"
	"testing"
)

func fixtureProvider() ModelProvider {
	return ModelProvider{ID: "p", SecretID: "s", Protocol: "openai_responses", BaseURL: "https://example.test/v1", DefaultModel: "m", Header: "Authorization", Template: "Bearer ${secret}"}
}
func TestBindingPreservesAndIsIdempotent(t *testing.T) {
	original := json.RawMessage(`{"secret_id":"other","files":[{"path":"x","value_template":"${secret}"}],"future":{"keep":true}}`)
	merged, changed, err := MergeProviderBinding([]json.RawMessage{original}, fixtureProvider())
	if err != nil || !changed || len(merged) != 2 || string(merged[0]) != string(original) {
		t.Fatalf("%v %v", changed, err)
	}
	again, changed, err := MergeProviderBinding(merged, fixtureProvider())
	if err != nil || changed || len(again) != 2 {
		t.Fatal("not idempotent", err)
	}
}
func TestBindingRefusesConflicts(t *testing.T) {
	raw := json.RawMessage(`{"secret_id":"other","egress_rules":[{"host_pattern":"example.test","action":"header","name":"authorization","value_template":"x"}]}`)
	if _, _, err := MergeProviderBinding([]json.RawMessage{raw}, fixtureProvider()); err == nil {
		t.Fatal("overwrote conflicting credential")
	}
}
func TestBindingRequiresScopedPath(t *testing.T) {
	p := fixtureProvider()
	p.BaseURL = "https://example.test"
	if _, _, err := MergeProviderBinding(nil, p); err == nil {
		t.Fatal("accepted unscoped host")
	}
}
