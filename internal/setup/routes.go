package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type AgentRoute struct {
	Agent   string `json:"agent"`
	Model   string `json:"model,omitempty"`
	Problem string `json:"problem,omitempty"`
}

// ParseAgentModel accepts only the documented CLI field response, not arbitrary
// config content. Some agents prefix the gateway name in their model reference.
func ParseAgentModel(output string) (string, error) {
	line := strings.TrimSpace(output)
	if strings.ContainsAny(line, "\r\n\x00") {
		return "", fmt.Errorf("invalid model response")
	}
	_, model, found := strings.Cut(line, " model ")
	if !found || model == "" || model == "default" {
		return "", fmt.Errorf("no explicit model selected")
	}
	model = strings.TrimPrefix(model, "magpie/")
	provider, name, ok := strings.Cut(model, "/")
	if !ok || provider == "" || name == "" || len(model) > 512 {
		return "", fmt.Errorf("model is not a gateway route")
	}
	return model, nil
}

func LocalAgentRoutes(ctx context.Context, agents []Agent) []AgentRoute {
	home, _ := os.UserHomeDir()
	binary := LocalMagpieBinary(home)
	routes := make([]AgentRoute, 0, len(agents))
	for _, agent := range agents {
		route := AgentRoute{Agent: agent.ID}
		if binary == "" {
			route.Problem = "local_gateway_not_found"
			routes = append(routes, route)
			continue
		}
		if !map[string]bool{"claude": true, "codex": true, "pi": true, "opencode": true, "kimi": true}[agent.ID] {
			route.Problem = "unsupported_agent"
			routes = append(routes, route)
			continue
		}
		if model, ok := readConfiguredMagpieModel(home, agent.ID); ok {
			route.Model = model
			routes = append(routes, route)
			continue
		}
		step, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(step, filepath.Clean(binary), agent.ID, "model")
		cmd.WaitDelay = time.Second
		output := &setupOutput{}
		cmd.Stdout = output
		cmd.Stderr = nil
		err := cmd.Run()
		cancel()
		if err != nil {
			route.Problem = "model_query_failed"
		} else {
			route.Model, err = ParseAgentModel(output.String())
			if err != nil {
				route.Problem = "model_selection_required"
			}
		}
		routes = append(routes, route)
	}
	return routes
}
