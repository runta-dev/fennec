package setup

import (
	"fennec/internal/runta"
	"strings"
	"testing"
)

func TestProviderRegistrationHasNoSecretValues(t *testing.T) {
	p := runta.ModelProvider{ID: "provider-id", Name: "Provider", SecretID: "secret-reference", DefaultModel: "model", Protocol: "openai_chat", BaseURL: "https://example.test/v1", Header: "Authorization", Template: "Bearer ${secret}"}
	script, id, err := GatewayProviderScript(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "fennec-") || !strings.Contains(script, "key=fennec-egress-managed") || strings.Contains(script, p.SecretID) || strings.Contains(script, "${secret}") {
		t.Fatal("unexpected registration script")
	}
	p.Header = "X-Custom"
	if _, _, err = GatewayProviderScript(p); err == nil {
		t.Fatal("unimplemented header accepted")
	}
}
