package runta

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// ModelProvider contains routing metadata and a secret reference, never a secret value.
type ModelProvider struct {
	ID           string `json:"id"`
	SecretID     string `json:"secret_id"`
	Name         string `json:"display_name"`
	Protocol     string `json:"protocol"`
	BaseURL      string `json:"base_url"`
	DefaultModel string `json:"default_model"`
	Header       string `json:"header_name"`
	Template     string `json:"value_template"`
}
type ProviderCatalog struct {
	OrganizationID string          `json:"organization_id"`
	Providers      []ModelProvider `json:"model_providers"`
}

func (c *Client) ModelProviders(ctx context.Context) (ProviderCatalog, error) {
	var response ProviderCatalog
	err := c.request(ctx, "GET", "/v2/model-providers", nil, "", &response)
	return response, err
}

// ValidateGatewayProvider verifies a routing candidate before generating config.
// Multiple candidates require an explicit selection; listing does not authorize
// attaching unrelated secrets or changing a runtime's existing secret rules.
func ValidateGatewayProvider(p ModelProvider) error {
	if p.ID == "" || p.SecretID == "" || p.DefaultModel == "" {
		return fmt.Errorf("provider identity, secret reference and model required")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return fmt.Errorf("provider requires an HTTPS base URL without credentials or query")
	}
	if strings.ContainsAny(p.Header, "\r\n:") || p.Header == "" || strings.ContainsAny(p.Template, "\r\n") {
		return fmt.Errorf("invalid credential injection metadata")
	}
	if !strings.Contains(p.Template, "${secret}") {
		return fmt.Errorf("provider credential template requires ${secret}")
	}
	switch p.Protocol {
	case "openai_chat", "openai_responses", "anthropic_messages":
		return nil
	default:
		return fmt.Errorf("unsupported provider protocol %q", p.Protocol)
	}
}
