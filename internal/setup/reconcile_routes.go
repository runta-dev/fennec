package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"os/exec"
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
	return parseGatewayModels(raw)
}

type GatewayModelItem struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Group    string `json:"group,omitempty"`
	Provider string `json:"provider,omitempty"`
	Account  string `json:"account,omitempty"`
}

func parseGatewayModels(raw []byte) ([]string, error) {
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

func parseGatewayCatalog(raw []byte, logins map[string]string) ([]GatewayModelItem, error) {
	var response struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			MagpieLabel string `json:"magpie_label"`
			OwnedBy     string `json:"owned_by"`
		} `json:"data"`
	}
	if len(raw) > 4<<20 || json.Unmarshal(raw, &response) != nil {
		return nil, fmt.Errorf("invalid model catalog")
	}
	items := make([]GatewayModelItem, 0, len(response.Data))
	for _, entry := range response.Data {
		if entry.ID == "" {
			continue
		}
		prefix, slug, hasSlash := strings.Cut(entry.ID, "/")
		if !hasSlash {
			prefix, slug = "", entry.ID
		}
		name := strings.TrimSpace(entry.DisplayName)
		if name == "" {
			name = slug
		}
		provider := prefix
		if provider == "group" || provider == "auto" || provider == "" {
			if entry.OwnedBy != "" {
				provider = entry.OwnedBy
			} else {
				provider = prefix
			}
		}
		group := strings.ToUpper(prefix)
		if prefix == "group" || prefix == "auto" || strings.HasSuffix(entry.MagpieLabel, "· routing group") {
			group = "ROUTING"
			provider = "magpie"
		} else if idx := strings.LastIndex(entry.MagpieLabel, " · "); idx >= 0 {
			lbl := strings.TrimSpace(entry.MagpieLabel[idx+len(" · "):])
			if lbl != "" {
				group = strings.ToUpper(lbl)
			}
		}
		acct := ""
		if logins != nil && prefix != "group" && prefix != "auto" {
			acct = logins[prefix]
			if acct == "" && entry.OwnedBy != "" {
				acct = logins[entry.OwnedBy]
			}
		}
		items = append(items, GatewayModelItem{
			ID:       entry.ID,
			Name:     name,
			Group:    group,
			Provider: provider,
			Account:  acct,
		})
	}
	return items, nil
}

type RouteResult struct {
	Agent  string `json:"agent"`
	Model  string `json:"model,omitempty"`
	Status string `json:"status"`
}

// ReconcileRoutes repairs routes individually and verifies their readback.
// Unknown local defaults remain explicit blockers, never invented fallbacks.
func ReconcileRoutes(ctx context.Context, client *ssh.Client, routes []AgentRoute) ([]RouteResult, error) {
	if len(routes) == 0 {
		return []RouteResult{}, nil
	}
	if err := ValidateDesiredRoutes(routes); err != nil {
		return nil, err
	}
	blocked := make([]RouteResult, 0, len(routes))
	for _, route := range routes {
		if route.Problem != "" {
			blocked = append(blocked, RouteResult{Agent: route.Agent, Model: route.Model, Status: route.Problem})
		}
	}
	if len(blocked) == len(routes) {
		return blocked, nil
	}
	data, _ := json.Marshal(routes)
	payload := base64.StdEncoding.EncodeToString(data)
	if len(payload) > 65536 {
		return nil, fmt.Errorf("oversized route request")
	}
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	data, err = session.Output(`"$HOME/.local/fennec/bin/fennec-terminal" reconcile-routes ` + payload)
	if err != nil {
		return nil, fmt.Errorf("route reconciliation not confirmed")
	}
	return DecodeRouteResults(data, routes)
}

func DecodeRouteResults(data []byte, routes []AgentRoute) ([]RouteResult, error) {
	var results []RouteResult
	if len(data) > 1<<20 || json.Unmarshal(data, &results) != nil || len(results) != len(routes) {
		return nil, fmt.Errorf("invalid route reconciliation result")
	}
	for i, result := range results {
		if result.Agent != routes[i].Agent || result.Model != routes[i].Model {
			return nil, fmt.Errorf("route result identity mismatch")
		}
		if routes[i].Problem != "" {
			if result.Status != routes[i].Problem {
				return nil, fmt.Errorf("route blocker was not preserved")
			}
		} else if result.Status != "configured" && result.Status != "configuration_failed" && result.Status != "verification_failed" {
			return nil, fmt.Errorf("invalid route result status")
		}
	}
	return results, nil
}

func ValidateDesiredRoutes(routes []AgentRoute) error {
	if len(routes) > 64 {
		return fmt.Errorf("too many routes")
	}
	for _, route := range routes {
		if route.Problem != "" {
			continue
		}
		provider, model, ok := strings.Cut(route.Model, "/")
		if !ok || len(route.Model) > 512 {
			return fmt.Errorf("invalid desired route")
		}
		if _, err := gatewayRouteScript(GatewayRoute{Provider: provider, Model: model, Agents: []string{route.Agent}}, true); err != nil {
			return err
		}
	}
	return nil
}

func ReconcileLocalRoutes(ctx context.Context, home string, routes []AgentRoute) ([]RouteResult, error) {
	if err := ValidateDesiredRoutes(routes); err != nil {
		return nil, err
	}
	readFn := func(ctx context.Context, agent string) ([]byte, error) {
		return readModelWithLock(ctx, home, agent)
	}
	writeShell := func(ctx context.Context, script string) error {
		command := exec.CommandContext(ctx, "/bin/sh", "-s")
		command.Stdin = strings.NewReader(script)
		return command.Run()
	}
	results := make([]RouteResult, 0, len(routes))
	for _, route := range routes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if route.Problem != "" {
			results = append(results, RouteResult{Agent: route.Agent, Model: route.Model, Status: route.Problem})
			continue
		}
		current, readErr := readFn(ctx, route.Agent)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return nil, readErr
		}
		if actual, parseErr := ParseAgentModel(string(current)); readErr == nil && parseErr == nil && actual == route.Model {
			results = append(results, RouteResult{Agent: route.Agent, Model: route.Model, Status: "configured"})
			continue
		}
		if handled, fastErr := writeConfiguredMagpieModel(ctx, home, route.Agent, route.Model); handled {
			if fastErr != nil {
				results = append(results, RouteResult{Agent: route.Agent, Model: route.Model, Status: "configuration_failed"})
				continue
			}
			output, verifyErr := readFn(ctx, route.Agent)
			actual, parseErr := ParseAgentModel(string(output))
			st := "configured"
			if verifyErr != nil || parseErr != nil || actual != route.Model {
				st = "verification_failed"
			}
			results = append(results, RouteResult{Agent: route.Agent, Model: route.Model, Status: st})
			continue
		}
		single, err := reconcileRoutesWith(ctx, []AgentRoute{route}, writeShell, readFn)
		if err != nil {
			return nil, err
		}
		results = append(results, single...)
	}
	return results, nil
}

func reconcileRoutesWith(ctx context.Context, routes []AgentRoute, write func(context.Context, string) error, read func(context.Context, string) ([]byte, error)) ([]RouteResult, error) {
	if err := ValidateDesiredRoutes(routes); err != nil {
		return nil, err
	}
	results := make([]RouteResult, 0, len(routes))
	for _, route := range routes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result := RouteResult{Agent: route.Agent, Model: route.Model, Status: route.Problem}
		if result.Status != "" {
			results = append(results, result)
			continue
		}
		// A successful readback already proves an unchanged route. Avoid the
		// write subprocess (which would repeat this same check) and second read.
		current, readErr := read(ctx, route.Agent)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return nil, readErr
		}
		if actual, parseErr := ParseAgentModel(string(current)); readErr == nil && parseErr == nil && actual == route.Model {
			result.Status = "configured"
			results = append(results, result)
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		provider, model, ok := strings.Cut(route.Model, "/")
		if !ok {
			return nil, fmt.Errorf("invalid desired route")
		}
		script, err := gatewayRouteScript(GatewayRoute{Provider: provider, Model: model, Agents: []string{route.Agent}}, true)
		if err != nil {
			return nil, err
		}
		if err = write(ctx, magpieWriteLock+script); err != nil {
			result.Status = "configuration_failed"
			results = append(results, result)
			continue
		}
		output, err := read(ctx, route.Agent)
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
