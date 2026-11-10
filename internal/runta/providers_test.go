package runta

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProviderCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/model-providers" || r.Method != "GET" {
			t.Fatal("wrong request")
		}
		fmt.Fprint(w, `{"organization_id":"org","model_providers":[{"id":"p","secret_id":"reference","protocol":"openai_responses","base_url":"https://example.test/v1","default_model":"model","header_name":"Authorization","value_template":"Bearer ${secret}"}]}`)
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL}
	catalog, err := c.ModelProviders(context.Background())
	if err != nil || len(catalog.Providers) != 1 {
		t.Fatal(err)
	}
	if err = ValidateGatewayProvider(catalog.Providers[0]); err != nil {
		t.Fatal(err)
	}
}
func TestRejectUnsafeProviderURL(t *testing.T) {
	for _, endpoint := range []string{"http://example.test", "https://user:password@example.test", "https://example.test?key=secret"} {
		if ValidateGatewayProvider(ModelProvider{ID: "p", SecretID: "s", DefaultModel: "m", BaseURL: endpoint, Protocol: "openai_responses", Header: "Authorization"}) == nil {
			t.Fatal(endpoint)
		}
	}
}

func TestProviderWireProtocolAndTemplate(t *testing.T) {
	p := fixtureProvider()
	p.Protocol = "openai_chat"
	if err := ValidateGatewayProvider(p); err != nil {
		t.Fatal(err)
	}
	p.Protocol = "openai_chat_completions"
	if ValidateGatewayProvider(p) == nil {
		t.Fatal("accepted non-Runta protocol")
	}
	p.Protocol = "openai_chat"
	p.Template = "Bearer ${credential}"
	if ValidateGatewayProvider(p) == nil {
		t.Fatal("accepted wrong template placeholder")
	}
}
