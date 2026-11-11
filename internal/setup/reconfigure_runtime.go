package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type RuntimeConfiguration struct {
	Accounts ModelAccountsRequest `json:"accounts"`
	Routes   []AgentRoute         `json:"routes,omitempty"`
}
type RuntimeConfigurationResult struct {
	NeedsSetup bool          `json:"needs_setup,omitempty"`
	Models     []string      `json:"models"`
	Routes     []RouteResult `json:"routes,omitempty"`
}

func (r RuntimeConfiguration) Validate() error {
	if len(r.Accounts.Agents) == 0 {
		return fmt.Errorf("agent inventory required")
	}
	if err := ValidateModelAccountsRequest(r.Accounts); err != nil {
		return err
	}
	if r.Accounts.Native && len(r.Routes) != 0 {
		return fmt.Errorf("native configuration cannot contain gateway routes")
	}
	if !r.Accounts.Native {
		wanted := map[string]bool{}
		for _, id := range r.Accounts.Agents {
			wanted[id] = true
		}
		for _, route := range r.Routes {
			if !wanted[route.Agent] {
				return fmt.Errorf("route inventory mismatch")
			}
			delete(wanted, route.Agent)
		}
		if len(wanted) != 0 {
			return fmt.Errorf("incomplete route inventory")
		}
	}
	return ValidateDesiredRoutes(r.Routes)
}

func DecodeRuntimeConfigurationResult(data []byte, request RuntimeConfiguration) (RuntimeConfigurationResult, error) {
	var result RuntimeConfigurationResult
	if err := request.Validate(); err != nil {
		return result, err
	}
	if len(data) > 4<<20 || json.Unmarshal(data, &result) != nil {
		return result, fmt.Errorf("invalid configuration result")
	}
	if result.NeedsSetup {
		if len(result.Routes) != 0 || len(result.Models) != 0 {
			return result, fmt.Errorf("ambiguous initialization result")
		}
		return result, nil
	}
	wanted := map[string]bool{}
	desired := map[string]string{}
	for _, route := range request.Routes {
		desired[route.Agent] = route.Model
	}
	for _, id := range request.Accounts.Agents {
		wanted[id] = true
	}
	for _, route := range result.Routes {
		if !wanted[route.Agent] || route.Status == "" {
			return result, fmt.Errorf("configuration result inventory mismatch")
		}
		if !request.Accounts.Native && route.Status == "configured" && route.Model != desired[route.Agent] {
			return result, fmt.Errorf("configured model does not match request")
		}
		delete(wanted, route.Agent)
	}
	if len(wanted) != 0 {
		return result, fmt.Errorf("incomplete configuration result")
	}
	return result, nil
}

// ReconfigureRuntime changes configuration on an already prepared machine.
// NeedsSetup is returned only before any mutation; errors after mutation are
// never permission to replay the request on another connection.
type runtimeConfigurationOps struct {
	models   func(context.Context) ([]string, error)
	accounts func(context.Context, string, ModelAccountsRequest) (ModelAccountsResult, error)
	routes   func(context.Context, string, []AgentRoute) ([]RouteResult, error)
}

func ReconfigureRuntime(ctx context.Context, home string, request RuntimeConfiguration) (RuntimeConfigurationResult, error) {
	return reconfigureRuntimeWith(ctx, home, request, runtimeConfigurationOps{LocalGatewayModels, ApplyModelAccounts, ReconcileLocalRoutes})
}
func reconfigureRuntimeWith(ctx context.Context, home string, request RuntimeConfiguration, ops runtimeConfigurationOps) (RuntimeConfigurationResult, error) {
	result := RuntimeConfigurationResult{}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := request.Validate(); err != nil {
		return result, err
	}
	for _, path := range []string{filepath.Join(home, ".local/fennec/node/bin/node"), filepath.Join(home, ".local/bin/herdr-core")} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return RuntimeConfigurationResult{NeedsSetup: true}, nil
		}
	}
	if err := exec.CommandContext(ctx, filepath.Join(home, ".local/bin/herdr-core"), "server", "status").Run(); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return RuntimeConfigurationResult{NeedsSetup: true}, nil
	}
	for _, agent := range request.Accounts.Agents {
		if !agentBinaryAvailable(home, agent, exec.LookPath) {
			return RuntimeConfigurationResult{NeedsSetup: true}, nil
		}
	}
	if !request.Accounts.Native {
		if _, err := exec.LookPath("magpie"); err != nil {
			return RuntimeConfigurationResult{NeedsSetup: true}, nil
		}
		check, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		_, err := ops.models(check)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			if errors.Is(err, syscall.ECONNREFUSED) {
				return RuntimeConfigurationResult{NeedsSetup: true}, nil
			}
			return result, fmt.Errorf("gateway catalog is not available: %w", err)
		}
	}
	request.Accounts.DeferCatalog = true
	if err := ctx.Err(); err != nil {
		return result, err
	}
	accounts, err := ops.accounts(ctx, home, request.Accounts)
	if err != nil {
		return result, err
	}
	if request.Accounts.Native {
		for _, agent := range request.Accounts.Agents {
			status := "credentials_required"
			if problem := request.Accounts.NativeProblems[agent]; problem != "" {
				status = problem
			}
			if accounts.NativeReady[agent] {
				status = "configured"
			}
			result.Routes = append(result.Routes, RouteResult{Agent: agent, Status: status})
		}
	} else {
		result.Routes, err = ops.routes(ctx, home, request.Routes)
		if err != nil {
			return result, fmt.Errorf("configuration readback failed: %w", err)
		}
		// Return the catalog after credentials/routes have been applied, not the
		// preflight snapshot from the previous configuration.
		result.Models, err = ops.models(ctx)
		if err != nil {
			return result, err
		}
		if result.Models == nil {
			result.Models = []string{}
		}
	}
	return result, nil
}
