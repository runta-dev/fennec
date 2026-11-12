package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/setup"
)

func (s *daemonServices) localModel(ctx context.Context, raw json.RawMessage) (any, error) {
	var params struct {
		Agent string `json:"agent"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return nil, &control.Fault{Code: "invalid_params", Message: "Choose an agent"}
	}
	if !newDesktopSettings(s.home).value.Magpie {
		return nil, &control.Fault{Code: "integration_disabled", Message: "Magpie is off"}
	}
	model, err := setup.LocalMagpieModel(ctx, s.home, params.Agent)
	if err != nil {
		return nil, err
	}
	models, _ := setup.LocalGatewayModels(ctx)
	catalog, _ := setup.LocalGatewayCatalog(ctx, s.home)
	return map[string]any{"agent": params.Agent, "model": model, "models": models, "catalog": catalog, "location": "local"}, nil
}
