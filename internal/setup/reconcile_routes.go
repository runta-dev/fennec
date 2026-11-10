package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"strings"
)

func GatewayModels(ctx context.Context, client *ssh.Client) ([]string, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	raw, err := session.Output("curl --fail --silent --max-time 15 http://127.0.0.1:3425/v1/models")
	if err != nil {
		return nil, fmt.Errorf("model catalog unavailable")
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if len(raw) > 4<<20 || json.Unmarshal(raw, &response) != nil {
		return nil, fmt.Errorf("invalid model catalog")
	}
	result := []string{}
	for _, entry := range response.Data {
		if entry.ID != "" {
			result = append(result, entry.ID)
		}
	}
	return result, nil
}

type RouteResult struct {
	Agent  string `json:"agent"`
	Model  string `json:"model,omitempty"`
	Status string `json:"status"`
}

// ReconcileRoutes repairs routes individually and verifies their readback.
// Unknown local defaults remain explicit blockers, never invented fallbacks.
func ReconcileRoutes(ctx context.Context, client *ssh.Client, routes []AgentRoute) ([]RouteResult, error) {
	results := make([]RouteResult, 0, len(routes))
	for _, route := range routes {
		result := RouteResult{Agent: route.Agent, Model: route.Model, Status: route.Problem}
		if result.Status != "" {
			results = append(results, result)
			continue
		}
		provider, model, ok := strings.Cut(route.Model, "/")
		if !ok {
			return nil, fmt.Errorf("invalid desired route")
		}
		script, err := gatewayRouteScript(GatewayRoute{Provider: provider, Model: model, Agents: []string{route.Agent}}, true)
		if err != nil {
			return nil, err
		}
		if err = Exec(ctx, client, magpieWriteLock+script); err != nil {
			result.Status = "configuration_failed"
			results = append(results, result)
			continue
		}
		verify, err := client.NewSession()
		if err != nil {
			return nil, err
		}
		stop := context.AfterFunc(ctx, func() { verify.Close() })
		output, err := verify.Output("export PATH=\"$HOME/.local/fennec/node/bin:$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH\"; magpie " + shellQuote(route.Agent) + " model")
		stop()
		verify.Close()
		actual, parseErr := ParseAgentModel(string(output))
		if err != nil || parseErr != nil || actual != route.Model {
			result.Status = "verification_failed"
		} else {
			result.Status = "configured"
		}
		results = append(results, result)
	}
	return results, nil
}

func probeFailureStatus(err error) string {
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitStatus() {
		case 75:
			return "model_rate_limited"
		case 76:
			return "model_temporarily_unavailable"
		case 77:
			return "model_authentication_required"
		case 78:
			return "model_access_denied"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "model_temporarily_unavailable"
	}
	return "model_unavailable"
}

// Shared provider/config mutations serialize across daemon processes as well.
// Inference probes deliberately run outside this lock.
const magpieWriteLock = `set -eu
umask 077
mkdir -p "$HOME/.local/fennec/install-locks"
exec 8>"$HOME/.local/fennec/install-locks/magpie-config.lock"
flock -w 60 8
`
