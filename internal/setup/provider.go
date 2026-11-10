package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"fennec/internal/runta"
)

// GatewayProviderScript registers only public routing metadata. Runta's egress
// gateway supplies the selected secret; the local gateway gets an inert token.
// Call only after BindProvider has successfully applied the matching egress rule.
func GatewayProviderScript(p runta.ModelProvider) (string, string, error) {
	if err := runta.ValidateGatewayProvider(p); err != nil {
		return "", "", err
	}
	// Magpie's custom provider key path emits Authorization or x-api-key. Do not
	// silently claim compatibility for an unrelated provider-specific header.
	expected := "Authorization"
	if p.Protocol == "anthropic_messages" {
		expected = "x-api-key"
	}
	if !strings.EqualFold(p.Header, expected) {
		return "", "", fmt.Errorf("provider requires a custom header adapter")
	}
	digest := sha256.Sum256([]byte(p.ID))
	id := "fennec-" + hex.EncodeToString(digest[:8])
	args := []string{"provider", "add", "Fennec " + p.Name, "id=" + id, "key=fennec-egress-managed", "models=" + p.DefaultModel}
	switch p.Protocol {
	case "anthropic_messages":
		args = append(args, "anthropic="+p.BaseURL)
	case "openai_responses":
		args = append(args, "url="+p.BaseURL, "responses="+p.BaseURL)
	case "openai_chat":
		args = append(args, "url="+p.BaseURL)
	}
	var script strings.Builder
	script.WriteString("set -eu\nexport PATH=\"$HOME/.local/fennec/node/bin:$HOME/.local/bin:$PATH\"\nmagpie")
	for _, arg := range args {
		script.WriteString(" " + shellQuote(arg))
	}
	script.WriteString("\n")
	return script.String(), id, nil
}
