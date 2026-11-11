package terminalstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRouteRPCServiceChild(t *testing.T) {
	if os.Getenv("FENNEC_ROUTE_RPC_CHILD") != "1" {
		return
	}
	runtime := cloudruntime.NewLocalRuntime(t.TempDir())
	if err := ServeWithOperations(context.Background(), os.Stdin, os.Stdout, nil, nil, nil, runtime.Operation); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestRoutesReuseResidentProtocol(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestRouteRPCServiceChild$")
	command.Env = append(os.Environ(), "FENNEC_ROUTE_RPC_CHILD=1")
	transport, err := Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if !transport.Supports("reconcile_routes") {
		t.Fatal("route capability missing")
	}
	if !transport.Supports("model_accounts") {
		t.Fatal("account capability missing")
	}
	_, err = transport.Operate(ctx, cloudruntime.LocalOperation{Kind: "model_accounts", Accounts: &setup.ModelAccountsRequest{Version: 1, Files: map[string]string{"unapproved.json": base64.StdEncoding.EncodeToString([]byte(`{"secret":"fixture-private-value"}`))}}})
	if err == nil {
		t.Fatal("invalid credential store accepted")
	}
	for _, agent := range []string{"codex", "pi"} {
		raw, err := transport.Operate(ctx, cloudruntime.LocalOperation{Kind: "reconcile_routes", Routes: []setup.AgentRoute{{Agent: agent, Problem: "model_selection_required"}}})
		var results []setup.RouteResult
		if err != nil || json.Unmarshal(raw, &results) != nil || len(results) != 1 || results[0].Agent != agent || results[0].Status != "model_selection_required" {
			t.Fatalf("resident route reply: %s %v", raw, err)
		}
	}
	if !transport.Alive() {
		t.Fatal("route operation closed shared transport")
	}
}

func TestNativeCredentialsReuseResidentChannelWithoutGateway(t *testing.T) {
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	command := exec.Command(executable, "-test.run=^TestRouteRPCServiceChild$")
	command.Env = append(os.Environ(), "FENNEC_ROUTE_RPC_CHILD=1")
	transport, e := Start(command)
	if e != nil {
		t.Fatal(e)
	}
	defer transport.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = transport.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	if !transport.Supports("native_accounts") {
		t.Fatal("native capability absent")
	}
	request := setup.ModelAccountsRequest{Version: 1, Native: true, Agents: []string{"codex"}, Files: map[string]string{"codex/auth.json": base64.StdEncoding.EncodeToString([]byte(`{"tokens":{"access_token":"fixture-secret"}}`)), "env.json": base64.StdEncoding.EncodeToString([]byte(`{}`))}}
	for i := 0; i < 2; i++ {
		raw, e := transport.Operate(ctx, cloudruntime.LocalOperation{Kind: "model_accounts", Accounts: &request})
		if e != nil {
			t.Fatal(e)
		}
		result, e := setup.DecodeModelAccountsResult(raw, len(request.Files))
		if e != nil || !result.NativeReady["codex"] {
			t.Fatal("native readiness missing", e)
		}
	}
	if !transport.Alive() {
		t.Fatal("native sync closed resident channel")
	}
}
