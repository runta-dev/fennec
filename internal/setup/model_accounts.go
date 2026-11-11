package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/crypto/ssh"
)

type ModelAccountsRequest struct {
	DeferCatalog bool              `json:"defer_catalog,omitempty"`
	Version      int               `json:"version"`
	Files        map[string]string `json:"files"`
	Plugins      []ModelPlugin     `json:"plugins"`
	Agents       []string          `json:"agents,omitempty"`
}

type ModelAccountsResult struct {
	CatalogDeferred  bool               `json:"catalog_deferred,omitempty"`
	Version          int                `json:"version"`
	Migrated         int                `json:"migrated"`
	Timings          map[string]float64 `json:"timings_ms"`
	Models           []string           `json:"models,omitempty"`
	CatalogAvailable bool               `json:"catalog_available"`
	AvailableAgents  map[string]bool    `json:"available_agents,omitempty"`
}

// Account material stays in authenticated stdin, never argv or diagnostics.
func SyncModelAccounts(ctx context.Context, client *ssh.Client, home string, plugins []ModelPlugin, agents ...[]string) (ModelAccountsResult, error) {
	request, err := GatherModelAccountsRequest(home, plugins, agents...)
	if err != nil {
		return ModelAccountsResult{}, err
	}
	return SyncModelAccountsRequest(ctx, client, request)
}

func GatherModelAccountsRequest(home string, plugins []ModelPlugin, agents ...[]string) (ModelAccountsRequest, error) {
	var result ModelAccountsRequest
	var ids []string
	if len(agents) > 1 {
		return result, fmt.Errorf("invalid agent inventory request")
	}
	if len(agents) == 1 {
		ids = agents[0]
	}
	if err := validateAgentInventory(ids); err != nil {
		return result, err
	}
	if err := validateModelPlugins(plugins); err != nil {
		return result, err
	}
	files, err := GatherCredentialPayload(home)
	if err != nil {
		return result, err
	}
	return ModelAccountsRequest{Version: 1, Files: files, Plugins: plugins, Agents: ids}, nil
}

func SyncModelAccountsRequest(ctx context.Context, client *ssh.Client, request ModelAccountsRequest) (ModelAccountsResult, error) {
	var result ModelAccountsResult
	if err := ValidateModelAccountsRequest(request); err != nil {
		return result, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return result, fmt.Errorf("cannot encode account synchronization")
	}
	session, err := client.NewSession()
	if err != nil {
		return result, err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	session.Stdin = bytes.NewReader(payload)
	data, err := session.Output(`"$HOME/.local/fennec/bin/fennec-terminal" sync-model-accounts`)
	if err != nil {
		return result, fmt.Errorf("model account synchronization not confirmed")
	}
	return DecodeModelAccountsResult(data, len(request.Files))
}

func DecodeModelAccountsResult(data []byte, files int) (ModelAccountsResult, error) {
	var result ModelAccountsResult
	if len(data) > 4<<20 || json.Unmarshal(data, &result) != nil || result.Version != 1 || result.Migrated != files {
		return ModelAccountsResult{}, fmt.Errorf("invalid account synchronization result")
	}
	return result, nil
}

func ReceiveModelAccounts(ctx context.Context, home string, input io.Reader) (ModelAccountsResult, error) {
	var request ModelAccountsRequest
	limited := &io.LimitedReader{R: input, N: (20 << 20) + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || limited.N == 0 {
		return ModelAccountsResult{}, fmt.Errorf("invalid account synchronization envelope")
	}
	return ApplyModelAccounts(ctx, home, request)
}

func ValidateModelAccountsRequest(request ModelAccountsRequest) error {
	if request.Version != 1 {
		return fmt.Errorf("unsupported account synchronization version")
	}
	if err := validateAgentInventory(request.Agents); err != nil {
		return err
	}
	return validateModelPlugins(request.Plugins)
}

func ApplyModelAccounts(ctx context.Context, home string, request ModelAccountsRequest) (ModelAccountsResult, error) {
	result, err := reconcileModelAccounts(ctx, home, request, ReconcileLocalModelPlugins, RestoreLocalCodexIdentity)
	if err != nil {
		return result, err
	}
	started := time.Now()
	result.CatalogDeferred = request.DeferCatalog
	if !request.DeferCatalog {
		result.Models, err = LocalGatewayModels(ctx)
		result.CatalogAvailable = err == nil
		result.Timings["catalog"] = float64(time.Since(started).Microseconds()) / 1000
	}
	started = time.Now()
	result.AvailableAgents = availableAgentInventory(home, request.Agents)
	result.Timings["inventory"] = float64(time.Since(started).Microseconds()) / 1000
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

func LocalGatewayModels(ctx context.Context) ([]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:3425/v1/models", nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("model catalog unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	return parseGatewayModels(data)
}

func reconcileModelAccounts(ctx context.Context, home string, request ModelAccountsRequest, plugins func(context.Context, string, []ModelPlugin) error, restore func(context.Context, string) error) (ModelAccountsResult, error) {
	result := ModelAccountsResult{Version: 1, Timings: map[string]float64{}}
	if request.Version != 1 {
		return result, fmt.Errorf("unsupported account synchronization version")
	}
	if err := validateAgentInventory(request.Agents); err != nil {
		return result, err
	}
	if err := validateModelPlugins(request.Plugins); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	started := time.Now()
	if len(request.Files) > 0 {
		data, err := json.Marshal(request.Files)
		if err != nil {
			return result, fmt.Errorf("invalid credential envelope")
		}
		if _, err := ReceiveCredentials(home, bytes.NewReader(data)); err != nil {
			return result, err
		}
	}
	result.Migrated = len(request.Files)
	result.Timings["credentials"] = float64(time.Since(started).Microseconds()) / 1000
	if err := ctx.Err(); err != nil {
		return result, err
	}
	started = time.Now()
	if err := plugins(ctx, home, request.Plugins); err != nil {
		return result, err
	}
	result.Timings["plugins"] = float64(time.Since(started).Microseconds()) / 1000
	if err := ctx.Err(); err != nil {
		return result, err
	}
	started = time.Now()
	if err := restore(ctx, home); err != nil {
		return result, err
	}
	result.Timings["identity"] = float64(time.Since(started).Microseconds()) / 1000
	return result, nil
}
