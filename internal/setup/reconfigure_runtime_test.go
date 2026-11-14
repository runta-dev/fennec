package setup

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReconfigurationDeclinesMissingToolsBeforeMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	result, err := ReconfigureRuntime(context.Background(), home, RuntimeConfiguration{Accounts: ModelAccountsRequest{Version: 1, Native: true, Agents: []string{"codex"}}})
	if err != nil || !result.NeedsSetup {
		t.Fatal(result, err)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatal("preflight refusal mutated the runtime", err)
	}
}
func TestNativeReconfigurationNeedsNoGatewayOrInstaller(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	binary, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".local/fennec/node/bin/node", ".local/bin/herdr-core"} {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(binary, path); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	request := RuntimeConfiguration{Accounts: ModelAccountsRequest{Version: 1, Native: true, Agents: []string{"codex"}, Files: map[string]string{"env.json": base64.StdEncoding.EncodeToString([]byte(`{}`)), "codex/auth.json": base64.StdEncoding.EncodeToString([]byte(`{"tokens":{"access_token":"fixture"}}`))}}}
	result, err := ReconfigureRuntime(context.Background(), home, request)
	if err != nil || result.NeedsSetup || len(result.Routes) != 1 || result.Routes[0].Status != "configured" {
		t.Fatal(result, err)
	}
	if !NativeAgentMode(home) {
		t.Fatal("native mode not applied")
	}
	if _, err := os.Stat(filepath.Join(home, ".config/magpie")); !os.IsNotExist(err) {
		t.Fatal("native switch touched gateway")
	}
}

func TestConfigurationResultCannotClaimIncompleteSuccessOrReplay(t *testing.T) {
	request := RuntimeConfiguration{Accounts: ModelAccountsRequest{Version: 1, Native: true, Agents: []string{"codex", "claude"}}}
	for _, raw := range []string{`{}`, `{"routes":[{"agent":"codex","status":"configured"}]}`, `{"routes":[{"agent":"codex","status":"configured"},{"agent":"codex","status":"configured"}]}`, `{"needs_setup":true,"routes":[{"agent":"codex","status":"configured"}]}`} {
		if _, err := DecodeRuntimeConfigurationResult([]byte(raw), request); err == nil {
			t.Fatalf("unsafe result accepted: %s", raw)
		}
	}
	result, err := DecodeRuntimeConfigurationResult([]byte(`{"routes":[{"agent":"codex","status":"configured"},{"agent":"claude","status":"credentials_required"}]}`), request)
	if err != nil || result.NeedsSetup {
		t.Fatal(result, err)
	}
}
func TestReconfigurationRejectsMixedModeAndMissingRoutes(t *testing.T) {
	for _, request := range []RuntimeConfiguration{
		{Accounts: ModelAccountsRequest{Version: 1, Native: true, Agents: []string{"codex"}}, Routes: []AgentRoute{{Agent: "codex", Model: "provider/model"}}},
		{Accounts: ModelAccountsRequest{Version: 1, Agents: []string{"codex"}}},
		{Accounts: ModelAccountsRequest{Version: 1, Agents: []string{"codex"}}, Routes: []AgentRoute{{Agent: "claude", Problem: "model_selection_required"}}},
	} {
		if request.Validate() == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func TestGatewayReconfigurationReadsBackCatalogAfterMutation(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	binary, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".local/fennec/node/bin/node", ".local/bin/herdr-core"} {
		path := filepath.Join(home, name)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.Symlink(binary, path); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"codex", "magpie", "cursor-agent", "grok"} {
		if err = os.Symlink(binary, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	request := RuntimeConfiguration{Accounts: ModelAccountsRequest{Version: 1, Agents: []string{"codex", "cursor", "grok"}}, Routes: []AgentRoute{{Agent: "codex", Model: "provider/new"}}}
	reads, writes := 0, 0
	ops := runtimeConfigurationOps{
		models: func(context.Context) ([]string, error) {
			reads++
			if writes == 0 {
				return []string{"provider/old"}, nil
			}
			return []string{"provider/new"}, nil
		},
		accounts: func(_ context.Context, _ string, r ModelAccountsRequest) (ModelAccountsResult, error) {
			writes++
			if !r.DeferCatalog {
				t.Fatal("duplicate catalog fetch")
			}
			return ModelAccountsResult{NativeReady: map[string]bool{"grok": true}}, nil
		},
		routes: func(_ context.Context, _ string, routes []AgentRoute) ([]RouteResult, error) {
			if len(routes) != 1 || routes[0].Agent != "codex" {
				t.Fatal("native agents were routed through the gateway", routes)
			}
			return []RouteResult{{Agent: "codex", Model: "provider/new", Status: "configured"}}, nil
		},
	}
	result, err := reconfigureRuntimeWith(context.Background(), home, request, ops)
	if err != nil || result.NeedsSetup || writes != 1 || reads != 2 || len(result.Models) != 1 || result.Models[0] != "provider/new" {
		t.Fatal(result, reads, writes, err)
	}
	if len(result.Routes) != 3 || result.Routes[1].Agent != "cursor" || result.Routes[1].Status != "credentials_required" || result.Routes[2].Agent != "grok" || result.Routes[2].Status != "configured" {
		t.Fatal("native account readiness was lost in mixed mode", result.Routes)
	}
	reads, writes = 0, 0
	ops.models = func(context.Context) ([]string, error) { return nil, context.DeadlineExceeded }
	result, err = reconfigureRuntimeWith(context.Background(), home, request, ops)
	if err == nil || result.NeedsSetup || writes != 0 {
		t.Fatal("slow read was mistaken for missing installation", result, err)
	}
	reads, writes = 0, 0
	ops.models = func(context.Context) ([]string, error) { return []string{}, nil }
	ops.accounts = func(context.Context, string, ModelAccountsRequest) (ModelAccountsResult, error) {
		writes++
		return ModelAccountsResult{}, context.Canceled
	}
	result, err = reconfigureRuntimeWith(context.Background(), home, request, ops)
	if err == nil || result.NeedsSetup || writes != 1 {
		t.Fatal("post-mutation failure allowed replay", result, err)
	}
}
