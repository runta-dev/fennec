package main

import (
	"encoding/json"
	"testing"
)

func TestRejectUnusableSuccessfulResponses(t *testing.T) {
	for _, catalog := range []string{`[]`, `[null]`, `[{}]`, `["gpt-test"]`, `[{"id":"gpt-test","name":" "}]`, `[{"id":"gpt-test","name":"GPT"},{"id":"gpt-test","name":"GPT"}]`} {
		t.Run(catalog, func(t *testing.T) {
			var models []json.RawMessage
			if err := json.Unmarshal([]byte(catalog), &models); err != nil {
				t.Fatal(err)
			}
			r := operationResult{Agent: "codex", Models: models}
			for _, method := range []string{"model.get"} {
				if err := r.validate(method, "codex", "cloud"); err == nil {
					t.Fatal("accepted unusable catalog", method)
				}
			}
		})
	}
}

func TestAccountLocationAndAvailability(t *testing.T) {
	for _, status := range []string{"signed_in", "signed_out"} {
		r := operationResult{Agent: "codex", Location: "local", Status: status}
		if err := r.validate("agent.account.get", "codex", "local"); err != nil {
			t.Fatal(err)
		}
		if r.validate("agent.account.get", "codex", "cloud") == nil {
			t.Fatal("accepted wrong account location")
		}
		if r.validate("agent.account.get", "claude", "local") == nil {
			t.Fatal("accepted wrong agent")
		}
	}
	r := operationResult{Agent: "codex", Location: "cloud", Status: "unavailable"}
	if r.validate("agent.account.get", "codex", "cloud") == nil {
		t.Fatal("accepted unavailable account")
	}
}

func TestUsableCatalog(t *testing.T) {
	r := operationResult{Agent: "codex", Models: []json.RawMessage{json.RawMessage(`{"id":"","name":"Default"}`), json.RawMessage(`{"id":"gpt-test","name":"GPT test"}`)}}
	if err := r.validate("model.get", "codex", "cloud"); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayCatalogContract(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`["openai/gpt-test"]`, true},
		{`[""]`, false}, {`[null]`, false}, {`[{}]`, false},
		{`["openai/gpt-test","openai/gpt-test"]`, false},
	} {
		var models []json.RawMessage
		if err := json.Unmarshal([]byte(tc.raw), &models); err != nil {
			t.Fatal(err)
		}
		r := operationResult{Agent: "codex", Models: models}
		if valid := r.validate("model.local.get", "codex", "local") == nil; valid != tc.valid {
			t.Fatalf("catalog %s: valid=%v", tc.raw, valid)
		}
	}
}
