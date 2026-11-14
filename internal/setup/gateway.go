package setup

import (
	"context"
	"fmt"
	"strings"
)

// GatewayRoute only contains a provider/model reference already registered in
// the gateway. It never accepts credentials, URLs or arbitrary shell fragments.
type GatewayRoute struct {
	Provider string
	Model    string
	Agents   []string
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

// GatewayRouteScript routes installed CLI agents via Magpie's own configuration
// writers, preserving its backups and per-agent schema handling. A successful
// config write alone is not an inference readiness check.
func GatewayRouteScript(route GatewayRoute) (string, error) {
	return gatewayRouteScript(route, false)
}

func gatewayRouteScript(route GatewayRoute, configurationOnly bool) (string, error) {
	if route.Provider == "" || route.Model == "" || strings.ContainsAny(route.Provider, "/\r\n\x00") || strings.ContainsAny(route.Model, "\r\n\x00") {
		return "", fmt.Errorf("valid provider/model required")
	}
	allowed := map[string]bool{"claude": true, "codex": true, "pi": true, "opencode": true, "kimi": true, "grok": true}
	seen := map[string]bool{}
	if len(route.Agents) == 0 {
		return "", fmt.Errorf("at least one agent required")
	}
	var script strings.Builder
	script.WriteString("set -eu\nexport PATH=\"$HOME/.local/fennec/node/bin:$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH\"\n")
	// Check the route is exposed before changing any agent configuration.
	if !configurationOnly {
		script.WriteString("\"$HOME/.local/fennec/bin/fennec-terminal\" verify-gateway-model " + shellQuote(route.Provider+"/"+route.Model) + "\n")
	}
	for _, id := range route.Agents {
		if !allowed[id] || seen[id] {
			return "", fmt.Errorf("unsupported or duplicate agent %q", id)
		}
		seen[id] = true
		script.WriteString("current=$(magpie " + shellQuote(id) + " model 2>/dev/null) || current=\n")
		script.WriteString("current=${current#* model }\ncurrent=${current#magpie/}\n")
		script.WriteString("if [ \"$current\" != " + shellQuote(route.Provider+"/"+route.Model) + " ]; then\nmagpie " + shellQuote(id) + " model " + shellQuote(route.Provider+"/"+route.Model) + "\nfi\n")
	}
	return script.String(), nil
}

func VerifyGatewayModel(ctx context.Context, model string) error {
	models, err := LocalGatewayModels(ctx)
	if err != nil {
		return err
	}
	for _, candidate := range models {
		if candidate == model {
			return nil
		}
	}
	return fmt.Errorf("selected model is not exposed by the gateway")
}
