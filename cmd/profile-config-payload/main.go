// profile-config-payload reads the same local inputs as reconfiguration and
// emits sizes only. It never sends credential payloads or changes configuration.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fennec/internal/agent"
	"fennec/internal/control"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var owner struct {
		ID string `json:"user_id"`
	}
	client := control.Client{Socket: filepath.Join(home, ".config/fennec/ipc/control.sock")}
	if err = client.Call(ctx, "account.profile", nil, &owner); err != nil {
		return fmt.Errorf("authorized profile unavailable")
	}
	if owner.ID == "" {
		return fmt.Errorf("account identity unavailable")
	}
	ids := agent.CloudAgentIDs()
	native, err := setup.GatherNativeAccounts(ctx, home, ids, true)
	if err != nil {
		return fmt.Errorf("native input collection failed")
	}
	plugins, err := setup.DesiredModelPlugins(home)
	if err != nil {
		return fmt.Errorf("plugin metadata unavailable")
	}
	gateway, err := setup.GatherModelAccountsRequest(home, plugins, ids)
	if err != nil {
		return fmt.Errorf("gateway input collection failed")
	}
	routes := make([]setup.AgentRoute, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			routes[i] = setup.LocalAgentRoutes(ctx, []setup.Agent{{ID: id, Version: "latest"}})[0]
		}(i, id)
	}
	wg.Wait()
	if err = ctx.Err(); err != nil {
		return err
	}
	routes, err = setup.ApplyModelPreferences(filepath.Join(home, ".config/fennec"), owner.ID, routes)
	if err != nil {
		return fmt.Errorf("model preferences unavailable")
	}
	gateway.NativeFiles = native.Files
	gateway.NativeProblems = native.NativeProblems
	request := setup.RuntimeConfiguration{Accounts: gateway, Routes: routes}
	if err = request.Validate(); err != nil {
		return fmt.Errorf("configuration inputs invalid")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	operation, err := json.Marshal(cloudruntime.LocalOperation{Kind: "reconfigure_runtime", Configuration: &request})
	if err != nil {
		return err
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return err
	}
	if _, err = writer.Write(operation); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	sizes := func(files map[string]string) map[string]int {
		out := map[string]int{}
		for name, value := range files {
			out[name] = len(value)
		}
		return out
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"configuration_json_bytes": len(payload), "operation_json_bytes": len(operation), "gzip_bytes": compressed.Len(), "gzip_base64_bytes": (compressed.Len() + 2) / 3 * 4, "native_base64_bytes": sizes(native.Files), "gateway_base64_bytes": sizes(gateway.Files)})
}
